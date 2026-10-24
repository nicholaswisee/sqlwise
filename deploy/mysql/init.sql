CREATE TABLE customers (
    id INT PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    name VARCHAR(120) NOT NULL,
    country CHAR(2) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE products (
    id INT PRIMARY KEY,
    sku VARCHAR(32) NOT NULL UNIQUE,
    name VARCHAR(120) NOT NULL,
    price DECIMAL(10,2) NOT NULL CHECK (price >= 0),
    thumbnail BLOB NULL
);

CREATE TABLE orders (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    customer_id INT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    placed_at DATETIME NOT NULL,
    note TEXT NULL,
    CONSTRAINT orders_customer_fk FOREIGN KEY (customer_id) REFERENCES customers (id),
    CONSTRAINT orders_status_check CHECK (status IN ('pending', 'paid', 'shipped')),
    INDEX orders_placed_at_idx (placed_at)
);

CREATE TABLE audit_log (
    event VARCHAR(64) NOT NULL,
    recorded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO customers (id, email, name, country)
WITH RECURSIVE seq (n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 250)
SELECT n, CONCAT('customer', n, '@example.com'), CONCAT('Customer ', n), IF(n % 7 = 0, NULL, ELT(n % 4 + 1, 'ID', 'SG', 'JP', 'NL')) FROM seq;

INSERT INTO products (id, sku, name, price, thumbnail) VALUES
    (1, 'TEA-01', 'Jasmine tea', 4.50, NULL),
    (2, 'KOPI-02', 'Kopi Luwak ', 32.00, NULL),
    (3, 'MOCHI-03', 'Mochi', 6.25, NULL);

INSERT INTO orders (customer_id, status, placed_at, note)
WITH RECURSIVE seq (n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 300)
SELECT n % 250 + 1, ELT(n % 3 + 1, 'pending', 'paid', 'shipped'), TIMESTAMP('2026-10-01') + INTERVAL n * 37 MINUTE,
    CASE WHEN n = 1 THEN REPEAT('Long delivery instructions. ', 400) WHEN n % 5 = 0 THEN 'Leave at the door' END
FROM seq;

INSERT INTO audit_log (event) VALUES ('seeded'), ('seeded'), ('demo ready');
