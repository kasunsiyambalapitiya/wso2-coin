-- Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
--
-- WSO2 LLC. licenses this file to you under the Apache License,
-- Version 2.0 (the "License"); you may not use this file except
-- in compliance with the License.
-- You may obtain a copy of the License at
--
-- http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing,
-- software distributed under the License is distributed on an
-- "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
-- KIND, either express or implied.  See the License for the
-- specific language governing permissions and limitations
-- under the License.

-- Swag Store schema, milestone 1. Every table carries the swag_ prefix (database team rule);
-- constraint names leave it out. Ids are CHAR(36) ascii, generated in Go (orders use UUIDv7).
-- Times are DATETIME(6) in UTC. Rows that orders point at are never hard-deleted.
-- user_email columns compare byte for byte (utf8mb4_bin): an accent-insensitive collation would let two
-- different addresses share a cart, orders or alerts.
-- swag_applied_migrations is created by the migrator itself, not here.

CREATE TABLE swag_categories (
    id         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    slug       VARCHAR(80)  NOT NULL,
    name       VARCHAR(120) NOT NULL,
    sort       INT          NOT NULL DEFAULT 0,
    created_at DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY categories_slug_uq (slug)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_products (
    id                CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    slug              VARCHAR(120)  NOT NULL,
    name              VARCHAR(200)  NOT NULL,
    description       VARCHAR(2000) NOT NULL DEFAULT '',
    category_id       CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
    price_coins       BIGINT        NOT NULL,
    restock_policy    ENUM('RESTOCKABLE','LIMITED') NOT NULL DEFAULT 'RESTOCKABLE',
    max_per_user      INT           NULL,
    limit_window_days INT           NULL,
    status            ENUM('DRAFT','ACTIVE','REMOVED') NOT NULL DEFAULT 'DRAFT',
    published_at      DATETIME(6)   NULL,
    removed_at        DATETIME(6)   NULL,
    removed_by        VARCHAR(320)  NULL,
    removal_note      VARCHAR(500)  NULL,
    created_at        DATETIME(6)   NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at        DATETIME(6)   NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY products_slug_uq (slug),
    KEY products_status_category_idx (status, category_id),
    CONSTRAINT products_category_fk FOREIGN KEY (category_id) REFERENCES swag_categories (id),
    CONSTRAINT products_price_ck CHECK (price_coins > 0),
    CONSTRAINT products_max_per_user_ck CHECK (max_per_user IS NULL OR max_per_user > 0),
    CONSTRAINT products_limit_window_ck CHECK (limit_window_days IS NULL OR limit_window_days > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_product_options (
    id         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    product_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    name       VARCHAR(60) NOT NULL,
    sort       INT         NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    UNIQUE KEY product_options_name_uq (product_id, name),
    CONSTRAINT product_options_product_fk FOREIGN KEY (product_id) REFERENCES swag_products (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_option_values (
    id         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    option_id  CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    value      VARCHAR(60) NOT NULL,
    swatch_hex CHAR(7)     NULL,
    sort       INT         NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    UNIQUE KEY option_values_value_uq (option_id, value),
    CONSTRAINT option_values_option_fk FOREIGN KEY (option_id) REFERENCES swag_product_options (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_product_images (
    id              CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    product_id      CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    url             VARCHAR(2048) NOT NULL,
    alt             VARCHAR(300)  NOT NULL DEFAULT '',
    option_value_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
    sort            INT           NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    KEY product_images_product_idx (product_id, sort),
    CONSTRAINT product_images_product_fk FOREIGN KEY (product_id) REFERENCES swag_products (id),
    CONSTRAINT product_images_value_fk FOREIGN KEY (option_value_id) REFERENCES swag_option_values (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_variants (
    id                CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    product_id        CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    sku               VARCHAR(64) NOT NULL,
    price_delta_coins BIGINT      NOT NULL DEFAULT 0,
    stock_on_hand     INT         NOT NULL DEFAULT 0,
    stock_reserved    INT         NOT NULL DEFAULT 0,
    restock_eta       DATE        NULL,
    active            BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at        DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at        DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY variants_sku_uq (sku),
    KEY variants_product_idx (product_id),
    CONSTRAINT variants_product_fk FOREIGN KEY (product_id) REFERENCES swag_products (id),
    CONSTRAINT variants_reserved_ck CHECK (stock_reserved <= stock_on_hand),
    CONSTRAINT variants_reserved_min_ck CHECK (stock_reserved >= 0),
    CONSTRAINT variants_on_hand_min_ck CHECK (stock_on_hand >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_variant_option_values (
    variant_id      CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    option_value_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    PRIMARY KEY (variant_id, option_value_id),
    CONSTRAINT variant_values_variant_fk FOREIGN KEY (variant_id) REFERENCES swag_variants (id),
    CONSTRAINT variant_values_value_fk FOREIGN KEY (option_value_id) REFERENCES swag_option_values (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_product_alternatives (
    product_id     CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    alternative_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    sort           INT NOT NULL DEFAULT 0,
    PRIMARY KEY (product_id, alternative_id),
    CONSTRAINT product_alternatives_product_fk FOREIGN KEY (product_id) REFERENCES swag_products (id),
    CONSTRAINT product_alternatives_alt_fk FOREIGN KEY (alternative_id) REFERENCES swag_products (id),
    CONSTRAINT product_alternatives_self_ck CHECK (product_id <> alternative_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_inventory_movements (
    id          BIGINT NOT NULL AUTO_INCREMENT,
    variant_id  CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    delta       INT    NOT NULL,
    reason      ENUM('RECEIVE','RESERVE','RELEASE','COMMIT','ADJUST','WRITE_OFF') NOT NULL,
    ref         VARCHAR(100) NOT NULL DEFAULT '',
    actor_email VARCHAR(320) NULL,
    created_at  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY inventory_movements_variant_idx (variant_id, created_at),
    CONSTRAINT inventory_movements_variant_fk FOREIGN KEY (variant_id) REFERENCES swag_variants (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_product_stats (
    product_id     CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    units_sold_30d INT         NOT NULL DEFAULT 0,
    updated_at     DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (product_id),
    CONSTRAINT product_stats_product_fk FOREIGN KEY (product_id) REFERENCES swag_products (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_cart_lines (
    id         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    user_email VARCHAR(320) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    variant_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    quantity   INT          NOT NULL,
    added_at   DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY cart_lines_user_variant_uq (user_email, variant_id),
    CONSTRAINT cart_lines_variant_fk FOREIGN KEY (variant_id) REFERENCES swag_variants (id),
    CONSTRAINT cart_lines_quantity_ck CHECK (quantity BETWEEN 1 AND 99)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_pickup_offices (
    id                CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    name              VARCHAR(120) NOT NULL,
    address           VARCHAR(500) NOT NULL DEFAULT '',
    country           VARCHAR(80)  NOT NULL DEFAULT '',
    collection_point  VARCHAR(200) NOT NULL DEFAULT '',
    hours             VARCHAR(200) NOT NULL DEFAULT '',
    lead_time_days    INT          NOT NULL DEFAULT 2,
    hr_locations_json JSON         NOT NULL,
    active            BOOLEAN      NOT NULL DEFAULT TRUE,
    sort              INT          NOT NULL DEFAULT 0,
    created_at        DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at        DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY pickup_offices_name_uq (name),
    CONSTRAINT pickup_offices_lead_time_ck CHECK (lead_time_days >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- One row per order number. SS-10001 is the first.
CREATE TABLE swag_order_numbers (
    n BIGINT NOT NULL AUTO_INCREMENT,
    PRIMARY KEY (n)
) ENGINE=InnoDB AUTO_INCREMENT=10001;

CREATE TABLE swag_orders (
    id                 CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    order_no           VARCHAR(20)  NOT NULL,
    user_email         VARCHAR(320) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    user_id            VARCHAR(128) NOT NULL,
    user_name          VARCHAR(200) NOT NULL DEFAULT '',
    status             ENUM('PENDING_PAYMENT','CONFIRMED','FAILED','READY_FOR_PICKUP','COLLECTED') NOT NULL,
    pickup_office_id   CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    office_name        VARCHAR(120) NOT NULL,
    wallet_address     VARCHAR(128) NOT NULL,
    total_coins        BIGINT       NOT NULL,
    payment_reference  VARCHAR(66) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    idempotency_key    CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    failure_code       VARCHAR(40)  NULL,
    attention_reason   VARCHAR(40)  NULL,
    payment_checks     INT          NOT NULL DEFAULT 0,
    placed_at          DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    confirmed_at       DATETIME(6)  NULL,
    failed_at          DATETIME(6)  NULL,
    ready_at           DATETIME(6)  NULL,
    collected_at       DATETIME(6)  NULL,
    collected_by_email VARCHAR(320) NULL,
    reminder_sent_at   DATETIME(6)  NULL,
    updated_at         DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY orders_no_uq (order_no),
    UNIQUE KEY orders_payment_reference_uq (payment_reference),
    UNIQUE KEY orders_idempotency_uq (user_email, idempotency_key),
    KEY orders_user_placed_idx (user_email, placed_at),
    KEY orders_status_placed_idx (status, placed_at),
    KEY orders_attention_idx (attention_reason),
    CONSTRAINT orders_office_fk FOREIGN KEY (pickup_office_id) REFERENCES swag_pickup_offices (id),
    CONSTRAINT orders_total_ck CHECK (total_coins > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_order_lines (
    id               CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    order_id         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    product_id       CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    variant_id       CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    sku              VARCHAR(64)  NOT NULL,
    name             VARCHAR(200) NOT NULL,
    options_label    VARCHAR(200) NOT NULL DEFAULT '',
    unit_price_coins BIGINT       NOT NULL,
    quantity         INT          NOT NULL,
    PRIMARY KEY (id),
    KEY order_lines_order_idx (order_id),
    KEY order_lines_product_idx (product_id),
    KEY order_lines_variant_idx (variant_id),
    CONSTRAINT order_lines_order_fk FOREIGN KEY (order_id) REFERENCES swag_orders (id),
    CONSTRAINT order_lines_product_fk FOREIGN KEY (product_id) REFERENCES swag_products (id),
    CONSTRAINT order_lines_variant_fk FOREIGN KEY (variant_id) REFERENCES swag_variants (id),
    CONSTRAINT order_lines_quantity_ck CHECK (quantity > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_order_events (
    id          BIGINT NOT NULL AUTO_INCREMENT,
    order_id    CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    from_status VARCHAR(20)   NULL,
    to_status   VARCHAR(20)   NOT NULL,
    note        VARCHAR(1000) NULL,
    actor_email VARCHAR(320)  NULL,
    created_at  DATETIME(6)   NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY order_events_order_idx (order_id, id),
    CONSTRAINT order_events_order_fk FOREIGN KEY (order_id) REFERENCES swag_orders (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_order_notes (
    id          BIGINT NOT NULL AUTO_INCREMENT,
    order_id    CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    note        VARCHAR(1000) NOT NULL,
    actor_email VARCHAR(320)  NOT NULL,
    created_at  DATETIME(6)   NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY order_notes_order_idx (order_id, id),
    CONSTRAINT order_notes_order_fk FOREIGN KEY (order_id) REFERENCES swag_orders (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- pending_variant_id is set only while the alert is PENDING, so the unique key allows one pending
-- alert per user and variant, and any number of closed ones.
CREATE TABLE swag_stock_alerts (
    id                 CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    user_email         VARCHAR(320) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    variant_id         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    status             ENUM('PENDING','NOTIFIED','CLOSED') NOT NULL DEFAULT 'PENDING',
    pending_variant_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin
                       GENERATED ALWAYS AS (IF(status = 'PENDING', variant_id, NULL)) STORED,
    created_at         DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    notified_at        DATETIME(6) NULL,
    closed_at          DATETIME(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY stock_alerts_pending_uq (user_email, pending_variant_id),
    KEY stock_alerts_variant_status_idx (variant_id, status),
    CONSTRAINT stock_alerts_variant_fk FOREIGN KEY (variant_id) REFERENCES swag_variants (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_notification_outbox (
    id              BIGINT NOT NULL AUTO_INCREMENT,
    template        VARCHAR(40)  NOT NULL,
    to_email        VARCHAR(320) NOT NULL,
    payload_json    JSON         NOT NULL,
    status          ENUM('PENDING','SENT','FAILED') NOT NULL DEFAULT 'PENDING',
    attempts        INT          NOT NULL DEFAULT 0,
    next_attempt_at DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_error      VARCHAR(256) NULL,
    dedupe_key      VARCHAR(120) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at      DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    sent_at         DATETIME(6)  NULL,
    PRIMARY KEY (id),
    UNIQUE KEY notification_outbox_dedupe_uq (dedupe_key),
    KEY notification_outbox_due_idx (status, next_attempt_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- No foreign keys: an admin action outlives what it describes.
CREATE TABLE swag_admin_actions (
    id            BIGINT NOT NULL AUTO_INCREMENT,
    actor_email   VARCHAR(320) NOT NULL,
    actor_user_id VARCHAR(128) NOT NULL,
    action        VARCHAR(60)  NOT NULL,
    entity_type   VARCHAR(40)  NOT NULL,
    entity_id     VARCHAR(64)  NOT NULL,
    detail_json   JSON         NOT NULL, -- JSON null when the action has no detail; SQL NULL would not scan
    created_at    DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY admin_actions_created_idx (created_at),
    KEY admin_actions_entity_idx (entity_type, entity_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE swag_job_state (
    job         VARCHAR(40)  NOT NULL,
    job_cursor  VARCHAR(100) NULL,
    last_run_at DATETIME(6)  NULL,
    last_status VARCHAR(20)  NULL,
    PRIMARY KEY (job)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
