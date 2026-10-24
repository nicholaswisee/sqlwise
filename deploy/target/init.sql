CREATE ROLE sqlwise_target LOGIN PASSWORD 'sqlwise_target_dev_only';
GRANT USAGE, CREATE ON SCHEMA public TO sqlwise_target;
CREATE SCHEMA sales AUTHORIZATION sqlwise_target;

SET ROLE sqlwise_target;

CREATE TABLE public.customers (
    id integer PRIMARY KEY,
    email varchar(255) NOT NULL UNIQUE,
    name text NOT NULL,
    country text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.products (
    id integer PRIMARY KEY,
    sku text NOT NULL UNIQUE,
    name text NOT NULL,
    price numeric(10,2) NOT NULL CHECK (price >= 0),
    thumbnail bytea
);

CREATE TABLE public.audit_log (
    event text NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sales.orders (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id integer NOT NULL REFERENCES public.customers(id),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid', 'shipped')),
    placed_at timestamptz NOT NULL,
    note text
);

CREATE INDEX orders_placed_at_idx ON sales.orders (placed_at);

CREATE TABLE sales.order_items (
    order_id bigint REFERENCES sales.orders(id),
    product_id integer REFERENCES public.products(id),
    quantity integer NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (order_id, product_id)
);

CREATE TABLE sales.customers (
    id integer PRIMARY KEY,
    region text NOT NULL
);

INSERT INTO public.customers (id, email, name, country)
SELECT g, 'customer' || g || '@example.com', 'Customer ' || g, CASE WHEN g % 7 = 0 THEN NULL ELSE (ARRAY['ID', 'SG', 'JP', 'NL'])[g % 4 + 1] END
FROM generate_series(1, 250) g;

INSERT INTO public.products (id, sku, name, price, thumbnail) VALUES
    (1, 'TEA-01', 'Jasmine tea', 4.50, '\x89504e470d0a1a0a'),
    (2, 'KOPI-02', 'Kopi Luwak ☕', 32.00, NULL),
    (3, 'MOCHI-03', '大福 mochi', 6.25, NULL),
    (4, 'EMPTY-04', '', 0, NULL);

INSERT INTO sales.orders (customer_id, status, placed_at, note)
SELECT (g % 250) + 1, (ARRAY['pending', 'paid', 'shipped'])[g % 3 + 1], timestamptz '2026-10-01' + g * interval '37 minutes',
    CASE WHEN g = 1 THEN repeat('Long delivery instructions. ', 400) WHEN g % 5 = 0 THEN 'Leave at the door' END
FROM generate_series(1, 300) g;

INSERT INTO sales.order_items (order_id, product_id, quantity)
SELECT o.id, (o.id % 3) + 1, (o.id % 4) + 1 FROM sales.orders o;

INSERT INTO sales.customers (id, region) VALUES (1, 'APAC'), (2, 'EMEA');

INSERT INTO public.audit_log (event) VALUES ('seeded'), ('seeded'), ('demo ready');

RESET ROLE;

CREATE SCHEMA IF NOT EXISTS sqlwise_internal;
CREATE TABLE IF NOT EXISTS sqlwise_internal.migration_receipts (
    attempt_id text PRIMARY KEY,
    database_id text NOT NULL,
    version bigint NOT NULL CHECK (version >= 1),
    sql_sha256 text NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('applied', 'fenced')),
    recorded_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON SCHEMA sqlwise_internal FROM PUBLIC;
REVOKE ALL ON sqlwise_internal.migration_receipts FROM PUBLIC;
GRANT USAGE ON SCHEMA sqlwise_internal TO "sqlwise_target";
GRANT SELECT, INSERT ON sqlwise_internal.migration_receipts TO "sqlwise_target";
