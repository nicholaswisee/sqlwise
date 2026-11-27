CREATE DATABASE delivery_orders;
CREATE DATABASE billing_ledger;
CREATE DATABASE dispatch_schedule;
CREATE DATABASE shipment_archive;

\connect delivery_orders

CREATE TABLE customers (
    id integer PRIMARY KEY,
    customer_name text NOT NULL,
    email text NOT NULL UNIQUE,
    phone text,
    created_at timestamptz NOT NULL
);

CREATE TABLE delivery_addresses (
    id integer PRIMARY KEY,
    customer_id integer NOT NULL REFERENCES customers(id),
    label text NOT NULL,
    address_line text NOT NULL,
    city text NOT NULL,
    country_code char(2) NOT NULL
);

CREATE TABLE orders (
    id integer PRIMARY KEY,
    customer_id integer NOT NULL REFERENCES customers(id),
    delivery_address_id integer NOT NULL REFERENCES delivery_addresses(id),
    status text NOT NULL CHECK (status IN ('placed', 'packed', 'delivered')),
    placed_at timestamptz NOT NULL,
    total numeric(10,2) NOT NULL CHECK (total >= 0)
);

INSERT INTO customers VALUES
    (1, 'Aisha Rahman', 'aisha.rahman@example.test', '+65 8123 4001', '2025-03-12 09:15:00+00'),
    (2, 'Kenji Sato', 'kenji.sato@example.test', '+81 90 2345 6002', '2025-04-02 14:30:00+00'),
    (3, 'Maya Chen', 'maya.chen@example.test', '+65 8777 1203', '2025-05-18 08:05:00+00'),
    (4, 'Ravi Menon', 'ravi.menon@example.test', '+91 98765 43004', '2025-06-21 17:40:00+00');

INSERT INTO delivery_addresses VALUES
    (1, 1, 'Home', '18 Tiong Bahru Road', 'Singapore', 'SG'),
    (2, 2, 'Office', '2-8-1 Nishi-Shinjuku', 'Tokyo', 'JP'),
    (3, 3, 'Home', '77 Telok Ayer Street', 'Singapore', 'SG'),
    (4, 4, 'Home', '14 MG Road', 'Bengaluru', 'IN');

INSERT INTO orders VALUES
    (1001, 1, 1, 'delivered', '2026-01-12 10:20:00+00', 48.50),
    (1002, 2, 2, 'packed', '2026-02-03 03:45:00+00', 126.00),
    (1003, 3, 3, 'placed', '2026-02-15 07:10:00+00', 32.75),
    (1004, 4, 4, 'delivered', '2026-03-01 11:00:00+00', 89.90),
    (1005, 1, 1, 'placed', '2026-03-09 05:35:00+00', 16.00);

\connect billing_ledger

CREATE TABLE invoices (
    id bigint PRIMARY KEY,
    customer_name text NOT NULL,
    invoice_number text NOT NULL UNIQUE,
    amount_due numeric(12,2) NOT NULL CHECK (amount_due >= 0),
    status text NOT NULL CHECK (status IN ('draft', 'issued', 'paid', 'overdue')),
    due_date date NOT NULL
);

INSERT INTO invoices VALUES
    (501, 'Aisha Rahman', 'INV-2026-0501', 48.50, 'paid', '2026-02-11'),
    (502, 'Kenji Sato', 'INV-2026-0502', 126.00, 'issued', '2026-03-05'),
    (503, 'Maya Chen', 'INV-2026-0503', 32.75, 'overdue', '2026-02-28'),
    (504, 'Ravi Menon', 'INV-2026-0504', 89.90, 'draft', '2026-04-01');

\connect dispatch_schedule

CREATE TABLE routes (
    id integer PRIMARY KEY,
    route_code text NOT NULL UNIQUE,
    origin text NOT NULL,
    destination text NOT NULL,
    driver_name text NOT NULL,
    scheduled_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('scheduled', 'in_transit', 'complete'))
);

INSERT INTO routes VALUES
    (31, 'SG-CBD-04', 'Tuas Depot', 'Tiong Bahru', 'Nur Hadi', '2026-03-10 01:30:00+00', 'complete'),
    (32, 'TOK-WEST-12', 'Shinagawa Hub', 'Shinjuku', 'Emi Tanaka', '2026-03-10 03:00:00+00', 'in_transit'),
    (33, 'BLR-CENTRAL-02', 'Peenya Depot', 'MG Road', 'Arjun Rao', '2026-03-10 04:15:00+00', 'scheduled');

\connect shipment_archive

CREATE TABLE shipments (
    id bigint PRIMARY KEY,
    tracking_code text NOT NULL UNIQUE,
    customer_name text NOT NULL,
    origin text NOT NULL,
    destination text NOT NULL,
    shipped_at date NOT NULL,
    delivered_at date,
    status text NOT NULL CHECK (status IN ('delivered', 'in_transit', 'delayed')),
    service_level text
);

INSERT INTO shipments
SELECT
    n,
    'SW-' || to_char(n, 'FM000000'),
    (ARRAY['Aisha Rahman', 'Kenji Sato', 'Maya Chen', 'Ravi Menon'])[1 + (n % 4)],
    (ARRAY['Singapore', 'Tokyo', 'Bengaluru', 'Kuala Lumpur'])[1 + (n % 4)],
    (ARRAY['Tokyo', 'Singapore', 'Mumbai', 'Jakarta'])[1 + ((n + 1) % 4)],
    date '2025-01-01' + (n % 420),
    CASE WHEN n % 11 = 0 THEN NULL ELSE date '2025-01-03' + (n % 420) END,
    CASE WHEN n % 11 = 0 THEN 'in_transit' WHEN n % 17 = 0 THEN 'delayed' ELSE 'delivered' END,
    NULL
FROM generate_series(1, 1000) AS n;
