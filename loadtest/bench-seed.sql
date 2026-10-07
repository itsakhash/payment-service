INSERT INTO accounts (external_ref, currency, balance_minor)
SELECT 'bench-payer-' || g, 'USD', 100000000 FROM generate_series(1, 100) g
ON CONFLICT DO NOTHING;

INSERT INTO accounts (external_ref, currency, balance_minor)
SELECT 'bench-merchant-' || g, 'USD', 0 FROM generate_series(1, 100) g
ON CONFLICT DO NOTHING;