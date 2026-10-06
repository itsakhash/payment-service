INSERT INTO accounts (external_ref, currency, balance_minor) VALUES
  ('payer-1', 'USD', 100000000),
  ('merchant-1', 'USD', 0)
ON CONFLICT (external_ref) DO NOTHING;