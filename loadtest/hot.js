import http from 'k6/http';
import { check } from 'k6';

const PAYER = Number(__ENV.PAYER || 1);
const PAYEE = Number(__ENV.PAYEE || 2);

export const options = {
  scenarios: {
    hot: { executor: 'constant-vus', vus: 50, duration: '20s' },
  },
  thresholds: { http_req_failed: ['rate<0.01'] },
};

export default function () {
  // Unique key per request; the timestamp keeps reruns from replaying old keys.
  const key = `${__VU}-${__ITER}-${Date.now()}`;
  const res = http.post(
    'http://localhost:8080/v1/payments',
    JSON.stringify({ payerAccountId: PAYER, payeeAccountId: PAYEE, amountMinor: 1, currency: 'USD' }),
    { headers: { 'Content-Type': 'application/json', 'X-Client-Id': 'k6', 'Idempotency-Key': key } }
  );
  check(res, { created: (r) => r.status === 201 });
}