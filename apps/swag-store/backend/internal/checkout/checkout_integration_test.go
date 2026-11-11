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

package checkout

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/coins"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/hr"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/orders"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/repository"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/reqctx"
	"github.com/wso2/wso2-coin/apps/swag-store/backend/internal/testdb"
)

type dbRig struct {
	repo      *repository.Store
	coins     *lockedCoins
	svc       *Service
	productID string
	variantID string
	officeID  string
}

// lockedCoins is fakeCoins made safe for concurrent orders.
type lockedCoins struct {
	mu sync.Mutex
	*fakeCoins
}

func (l *lockedCoins) Pay(ctx context.Context, r coins.PayRequest) (*coins.Payment, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fakeCoins.Pay(ctx, r)
}

func newDBRig(t *testing.T, stock int) *dbRig {
	t.Helper()
	ctx := context.Background()
	a := repository.Actor{Email: "admin@wso2.com", UserID: "u-admin"}
	repo := repository.New(testdb.Open(t))
	d, err := repo.CreateProduct(ctx, a, repository.ProductInput{Name: "WSO2 Hoodie", Slug: "wso2-hoodie", Price: 90, RestockPolicy: "RESTOCKABLE"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetProductStatus(ctx, a, d.Product.ID, "ACTIVE", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordStockMovement(ctx, a, d.Variants[0].ID, repository.MoveReceive, stock, ""); err != nil {
		t.Fatal(err)
	}
	o, err := repo.SaveOffice(ctx, a, repository.Office{Name: "Colombo office", Active: true, HRLocations: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	c := &lockedCoins{fakeCoins: newFakeCoins()}
	svc := NewService(repo, c, &fakeHR{emp: &hr.Employee{EmployeeStatus: "Active"}}, NewTreasuryGuard(pin, c))
	return &dbRig{repo: repo, coins: c, svc: svc, productID: d.Product.ID, variantID: d.Variants[0].ID, officeID: o.ID}
}

func shopperCtx(email string) (context.Context, *reqctx.User) {
	u := &reqctx.User{Email: email, UserID: "u-" + email}
	return reqctx.WithUserAssertion(reqctx.WithUser(context.Background(), u), "jwt"), u
}

func TestLastUnitRace(t *testing.T) {
	r := newDBRig(t, 1)
	const buyers = 20
	var wg sync.WaitGroup
	results := make([]error, buyers)
	statuses := make([]orders.Status, buyers)
	for i := 0; i < buyers; i++ {
		ctx, u := shopperCtx(uuid.NewString() + "@wso2.com")
		if _, err := r.repo.AddToCart(ctx, u.Email, r.variantID, 1); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(i int, ctx context.Context, u *reqctx.User) {
			defer wg.Done()
			o, err := r.svc.Place(ctx, PlaceInput{User: u, OfficeID: r.officeID, WalletAddress: userWall, IdempotencyKey: uuid.NewString()})
			results[i] = err
			if o != nil {
				statuses[i] = o.Status
			}
		}(i, ctx, u)
	}
	wg.Wait()
	confirmed, refused := 0, 0
	for i := range results {
		switch {
		case results[i] == nil && statuses[i] == orders.Confirmed:
			confirmed++
		case errors.Is(results[i], ErrQuoteChanged):
			refused++
		default:
			t.Errorf("buyer %d: %v %s", i, results[i], statuses[i])
		}
	}
	if confirmed != 1 || refused != buyers-1 {
		t.Fatalf("confirmed %d, refused %d", confirmed, refused)
	}
	d, _ := r.repo.ProductByID(context.Background(), r.productID)
	if v := d.Variants[0]; v.OnHand != 0 || v.Reserved != 0 {
		t.Fatalf("stock = %d on hand, %d reserved", v.OnHand, v.Reserved)
	}
}

func TestRemovalRacingCheckout(t *testing.T) {
	r := newDBRig(t, 5)
	ctx, u := shopperCtx("you@wso2.com")
	if _, err := r.repo.AddToCart(ctx, u.Email, r.variantID, 1); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var placeErr, removeErr error
	var placed *repository.Order
	wg.Add(2)
	go func() {
		defer wg.Done()
		placed, placeErr = r.svc.Place(ctx, PlaceInput{User: u, OfficeID: r.officeID, WalletAddress: userWall, IdempotencyKey: uuid.NewString()})
	}()
	go func() {
		defer wg.Done()
		removeErr = r.repo.SetProductStatus(context.Background(), repository.Actor{Email: "a@wso2.com", UserID: "a"}, r.productID, "REMOVED", "")
	}()
	wg.Wait()
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	d, _ := r.repo.ProductByID(context.Background(), r.productID)
	v := d.Variants[0]
	switch {
	case placeErr == nil && placed.Status == orders.Confirmed:
		if v.OnHand != 4 || v.Reserved != 0 {
			t.Fatalf("placed before removal, stock %d/%d", v.OnHand, v.Reserved)
		}
	case errors.Is(placeErr, ErrQuoteChanged):
		if v.OnHand != 5 || v.Reserved != 0 || len(r.coins.pays) != 0 {
			t.Fatalf("refused, but stock %d/%d pays %d", v.OnHand, v.Reserved, len(r.coins.pays))
		}
	default:
		t.Fatalf("half done: %+v %v", placed, placeErr)
	}
}

func TestOrderIdempotencyReplay(t *testing.T) {
	r := newDBRig(t, 5)
	ctx, u := shopperCtx("you@wso2.com")
	if _, err := r.repo.AddToCart(ctx, u.Email, r.variantID, 1); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	first, err := r.svc.Place(ctx, PlaceInput{User: u, OfficeID: r.officeID, WalletAddress: userWall, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.svc.Place(ctx, PlaceInput{User: u, OfficeID: r.officeID, WalletAddress: userWall, IdempotencyKey: key})
	if err != nil || second.ID != first.ID || second.Status != orders.Confirmed {
		t.Fatalf("replay = %+v %v", second, err)
	}
	if len(r.coins.pays) != 1 {
		t.Fatalf("/payments called %d times", len(r.coins.pays))
	}
	if n, _ := r.repo.CartItemCount(ctx, u.Email); n != 0 {
		t.Fatalf("cart not emptied: %d", n)
	}
}
