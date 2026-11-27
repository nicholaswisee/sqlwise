CREATE TABLE products (
    id integer PRIMARY KEY,
    sku varchar(32) NOT NULL UNIQUE,
    product_name varchar(120) NOT NULL,
    category varchar(60) NOT NULL,
    unit_price decimal(10,2) NOT NULL CHECK (unit_price >= 0)
);

CREATE TABLE stock_levels (
    product_id integer PRIMARY KEY REFERENCES products(id),
    warehouse_code varchar(16) NOT NULL,
    quantity integer NOT NULL CHECK (quantity >= 0),
    reorder_at integer NOT NULL CHECK (reorder_at >= 0),
    counted_at datetime NOT NULL
);

INSERT INTO products VALUES
    (1, 'TEA-001', 'Jasmine green tea 100g', 'Pantry', 8.50),
    (2, 'COF-014', 'Sumatra whole bean coffee 250g', 'Pantry', 16.90),
    (3, 'MUG-203', 'Stoneware travel mug', 'Kitchen', 24.00),
    (4, 'BAG-088', 'Canvas market tote', 'Accessories', 12.75),
    (5, 'SOAP-031', 'Citrus hand soap 300ml', 'Home', 7.20),
    (6, 'NOTE-115', 'Recycled paper notebook', 'Stationery', 5.40);

INSERT INTO stock_levels VALUES
    (1, 'SG-WH-01', 84, 20, '2026-03-09 08:00:00'),
    (2, 'SG-WH-01', 13, 18, '2026-03-09 08:10:00'),
    (3, 'JP-WH-02', 42, 10, '2026-03-09 08:20:00'),
    (4, 'SG-WH-01', 7, 12, '2026-03-09 08:30:00'),
    (5, 'IN-WH-03', 31, 8, '2026-03-09 08:40:00'),
    (6, 'JP-WH-02', 56, 15, '2026-03-09 08:50:00');
