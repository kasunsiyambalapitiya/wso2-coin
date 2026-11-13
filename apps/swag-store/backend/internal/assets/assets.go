// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

// Package assets stores product photos in an Azure Blob container and signs
// the read-only URLs browsers load them from.
//
// The backend writes with its own Microsoft Entra identity (a managed
// identity or an app registration) and signs read URLs with a user-delegation
// key from that identity (one read SAS per photo, for that blob only), so no
// SAS or account key is configured. A shared
// account key is accepted only for the local emulator.
package assets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
)

// Health values, as /health/dependencies reports them.
const (
	HealthOK            = "ok"
	HealthNotConfigured = "not_configured"
	HealthKeyMissing    = "key_unavailable"
)

// photoCacheControl: each URL lives about a day (one read SAS per UTC day),
// and each blob name is used once.
const photoCacheControl = "public, max-age=86400"

var (
	// ErrNotConfigured means photo storage isn't set up in this environment.
	ErrNotConfigured = errors.New("assets: not configured")
	// ErrExists means a blob with that name is already there. Names are random,
	// so the caller draws a new one.
	ErrExists = errors.New("assets: blob already exists")
)

// Config is where photos go and which identity writes them.
type Config struct {
	AccountURL string // https://<account>.blob.core.windows.net, or the emulator's loopback URL
	Container  string
	AccountKey string // emulator only; config refuses it off loopback
	TenantID   string
	ClientID   string // an app registration, or a user-assigned managed identity
	// ClientSecret selects an app registration; without it the managed identity is used.
	ClientSecret string
	Timeout      time.Duration
}

// signFunc signs SAS values with whatever key the client holds.
type signFunc func(sas.BlobSignatureValues) (sas.QueryParameters, error)

// keyFetcher asks Azure for a user-delegation key valid from start to expiry.
type keyFetcher func(ctx context.Context, start, expiry time.Time) (signFunc, error)

// Client writes photos and signs read URLs. The zero value is not usable; use
// New or Disabled.
type Client struct {
	disabled   bool
	accountURL string
	container  string
	protocol   sas.Protocol
	timeout    time.Duration
	blobs      *container.Client
	fetchKey   keyFetcher // nil with a shared key
	now        func() time.Time

	mu        sync.Mutex
	sign      signFunc
	signUntil time.Time // zero: the key never expires (shared key)
	sasDay    time.Time
	sasQuery  map[string]string // today's SAS per photo path
	keyErrLog time.Time
}

// Disabled is the client used when photo storage isn't configured: uploads
// fail with ErrNotConfigured and every photo URL is nil.
func Disabled() *Client { return &Client{disabled: true} }

// New builds a client. It makes no network call: the first delegation key is
// fetched by Start, so a Blob outage or a missing role never stops startup.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.AccountURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("assets: bad account URL")
	}
	if cfg.Container == "" {
		return nil, fmt.Errorf("assets: no container")
	}
	c := &Client{
		accountURL: strings.TrimRight(cfg.AccountURL, "/"),
		container:  cfg.Container,
		protocol:   sas.ProtocolHTTPS,
		timeout:    cfg.Timeout,
	}
	if u.Scheme == "http" {
		c.protocol = sas.ProtocolHTTPSandHTTP // the emulator on loopback
	}
	if c.timeout <= 0 {
		c.timeout = 25 * time.Second
	}
	// No SDK retries: a failed upload is reported to the admin, who tries again.
	opts := &service.ClientOptions{ClientOptions: azcore.ClientOptions{Retry: policy.RetryOptions{MaxRetries: -1}}}

	var svc *service.Client
	if cfg.AccountKey != "" {
		cred, err := azblob.NewSharedKeyCredential(accountName(u), cfg.AccountKey)
		if err != nil {
			return nil, fmt.Errorf("assets: bad account key: %w", err)
		}
		if svc, err = service.NewClientWithSharedKeyCredential(c.accountURL, cred, opts); err != nil {
			return nil, err
		}
		c.sign = func(v sas.BlobSignatureValues) (sas.QueryParameters, error) { return v.SignWithSharedKey(cred) }
	} else {
		var cred azcore.TokenCredential
		if cfg.ClientSecret != "" {
			cred, err = azidentity.NewClientSecretCredential(cfg.TenantID, cfg.ClientID, cfg.ClientSecret, nil)
		} else {
			mi := &azidentity.ManagedIdentityCredentialOptions{}
			if cfg.ClientID != "" {
				mi.ID = azidentity.ClientID(cfg.ClientID)
			}
			cred, err = azidentity.NewManagedIdentityCredential(mi)
		}
		if err != nil {
			return nil, fmt.Errorf("assets: credential: %w", err)
		}
		if svc, err = service.NewClient(c.accountURL, cred, opts); err != nil {
			return nil, err
		}
		c.fetchKey = func(ctx context.Context, start, expiry time.Time) (signFunc, error) {
			udc, err := svc.GetUserDelegationCredential(ctx, service.KeyInfo{
				Start:  to.Ptr(start.UTC().Format(sas.TimeFormat)),
				Expiry: to.Ptr(expiry.UTC().Format(sas.TimeFormat)),
			}, nil)
			if err != nil {
				return nil, describe("delegation key", err)
			}
			return func(v sas.BlobSignatureValues) (sas.QueryParameters, error) { return v.SignWithUserDelegation(udc) }, nil
		}
	}
	c.blobs = svc.NewContainerClient(cfg.Container)
	return c, nil
}

// accountName is the storage account's name: the emulator puts it in the
// path (http://127.0.0.1:10000/devstoreaccount1), Azure in the host.
func accountName(u *url.URL) string {
	if seg := strings.Split(strings.Trim(u.Path, "/"), "/")[0]; seg != "" {
		return seg
	}
	return strings.Split(u.Hostname(), ".")[0]
}

// Put stores one photo under path. It never overwrites: an existing blob
// gives ErrExists.
func (c *Client) Put(ctx context.Context, path string, body []byte, contentType string) error {
	if c.disabled {
		return ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	_, err := c.blobs.NewBlockBlobClient(path).Upload(ctx, streaming.NopCloser(bytes.NewReader(body)), &blockblob.UploadOptions{
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: to.Ptr(contentType), BlobCacheControl: to.Ptr(photoCacheControl)},
		AccessConditions: &blob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: to.Ptr(azcore.ETagAny)},
		},
	})
	if bloberror.HasCode(err, bloberror.BlobAlreadyExists) {
		return ErrExists
	}
	if err != nil {
		return describe("upload", err)
	}
	return nil
}

// EnsureContainer creates the container if it isn't there. Only the local
// seed uses it; in Azure, ops create the container.
func (c *Client) EnsureContainer(ctx context.Context) error {
	if c.disabled {
		return ErrNotConfigured
	}
	_, err := c.blobs.Create(ctx, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
		return describe("create container", err)
	}
	return nil
}

// Health reports whether photos can be signed right now. It makes no
// network call.
func (c *Client) Health() string {
	if c.disabled {
		return HealthNotConfigured
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keyUsable(c.clock()) {
		return HealthOK
	}
	return HealthKeyMissing
}

// describe turns an Azure error into one without the request URL, which
// could carry a query string.
func describe(op string, err error) error {
	var re *azcore.ResponseError
	if errors.As(err, &re) {
		return fmt.Errorf("assets: %s failed: %d %s", op, re.StatusCode, re.ErrorCode)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("assets: %s failed: %w", op, err)
	}
	return fmt.Errorf("assets: %s failed", op)
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
