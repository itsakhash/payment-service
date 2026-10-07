import http from 'k6/http';
import { check } from 'k6';

export const options = {
    scenarios: {
        reads: { executor: 'constant-vus', vus: 50, duration: '20s' },
    },
    thresholds: { http_req_failed: ['rate<0.01'] },
};

export function setup() {
    const ids = [];
    for (let i = 0; i < 200; i++) {
        const res = http.post(
            'http://localhost:8080/v1/payments',
            JSON.stringify({ payerAccountId: 1, payeeAccountId: 2, amountMinor: 1, currency: 'USD' }),
            { headers: { 'Content-Type': 'application/json', 'X-Client-Id': 'k6-setup', 'Idempotency-Key': `get-setup-${Date.now()}-${i}` } }
        );
        ids.push(res.json('paymentId'));
    }
    return { ids };
}

export default function (data) {
    const id = data.ids[Math.floor(Math.random() * data.ids.length)];
    const res = http.get(`http://localhost:8080/v1/payments/${id}`);
    check(res, { ok: (r) => r.status === 200 });
}