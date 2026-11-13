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

-- Product photos are blob paths in the photo container, one per view
-- (products/{productId}/{view}-{8 hex}.{jpg|png|webp}). Earlier rows held pasted links,
-- which have no blob behind them, so the table starts empty.
DROP TABLE IF EXISTS swag_product_images;

CREATE TABLE swag_product_images (
    id              CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    product_id      CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    photo_view      VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    path            VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    alt             VARCHAR(300)  NOT NULL DEFAULT '',
    option_value_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
    sort            INT           NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    UNIQUE KEY product_images_view_uq (product_id, photo_view),
    KEY product_images_product_idx (product_id, sort),
    CONSTRAINT product_images_product_fk FOREIGN KEY (product_id) REFERENCES swag_products (id),
    CONSTRAINT product_images_value_fk FOREIGN KEY (option_value_id) REFERENCES swag_option_values (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
