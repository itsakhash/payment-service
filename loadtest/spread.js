import http from 'k6/http';
import { check } from 'k6';

const PAYER_MIN = Number(__ENV.PAYER_MIN);
const MERCH_MIN = Number(__ENV.MERCH_MIN);

export const options = {
  scenarios: {
    spread: { executor: 'constant-vus', vus: 50, duration: '20s' },
  },
  thresholds: { http_req_failed: ['rate<0.01'] },
};

export default function () {
  // Each virtual user pays between its own private pair of accounts.
  const payer = PAYER_MIN + (__VU - 1);
  const payee = MERCH_MIN + (__VU - 1);
  const key = `${__VU}-${__ITER}-${Date.now()}`;
  const res = http.post(
    'http://localhost:8080/v1/payments',
    JSON.stringify({ payerAccountId: payer, payeeAccountId: payee, amountMinor: 1, currency: 'USD' }),
    { headers: { 'Content-Type': 'application/json', 'X-Client-Id': 'k6', 'Idempotency-Key': key } }
  );
  check(res, { created: (r) => r.status === 201 });
}