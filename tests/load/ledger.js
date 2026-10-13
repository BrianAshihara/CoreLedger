// Teste de carga do CoreLedger (SPEC Fase 6), para o k6.
//
// Cenários (variável SCENARIO):
//   transfers  só transferências entre contas USER aleatórias
//   deposits   só depósitos: todos travam a mesma linha (a conta WORLD) e
//              mostram o gargalo documentado no ADR 0004
//   mixed      90% transferências, 10% depósitos (padrão)
//
// A massa de dados é criada pela própria API no setup: ACCOUNTS contas, cada
// uma com um depósito inicial. Saldo insuficiente é um resultado de negócio
// esperado e não conta como erro. Qualquer outra resposta inesperada conta, e
// a meta é zero.
//
// Exemplo:
//   k6 run -e SCENARIO=transfers -e VUS=20 -e DURATION=30s tests/load/ledger.js

import http from 'k6/http';
import { check, fail } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const ADMIN_TOKEN = __ENV.ADMIN_TOKEN || 'dev-admin-token-change-me';
const SCENARIO = __ENV.SCENARIO || 'mixed';
const ACCOUNTS = Number(__ENV.ACCOUNTS || 200);
const VUS = Number(__ENV.VUS || 20);
const DURATION = __ENV.DURATION || '30s';
const INITIAL_BALANCE = 1000000; // R$ 10.000,00 por conta

if (!['transfers', 'deposits', 'mixed'].includes(SCENARIO)) {
  throw new Error(`unknown SCENARIO ${SCENARIO}`);
}

export const options = {
  setupTimeout: '5m',
  scenarios: {
    load: { executor: 'constant-vus', vus: VUS, duration: DURATION },
  },
  thresholds: {
    unexpected_responses: ['count==0'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

const unexpected = new Counter('unexpected_responses');
// No teste de reinício do PostgreSQL, a API deve responder 503 (repetível)
// enquanto o banco está fora, e nunca 500.
const serviceBusy = new Counter('service_busy_503');
const serverErrors = new Counter('server_errors_5xx_other');
const insufficientFunds = new Counter('insufficient_funds');
// Um 200 com Idempotent-Replayed: true é resultado válido: o cliente HTTP do
// k6 (escrito em Go) reenvia sozinho requisições com Idempotency-Key quando
// uma conexão cai, e o servidor responde com a transação original sem mover o
// dinheiro de novo. Foi observado assim num teste de carga real.
const replays = new Counter('idempotent_replays');
const transfersOK = new Counter('transfers_ok');
const depositsOK = new Counter('deposits_ok');
const transferDuration = new Trend('transfer_duration', true);
const depositDuration = new Trend('deposit_duration', true);

const JSON_HEADERS = { 'Content-Type': 'application/json' };

// Chave única por requisição: execução + VU + contador. Cada VU tem a sua
// cópia do módulo, então o contador é por VU (no setup, __VU vale 0). O
// __ITER não serve aqui porque não existe durante o setup.
const RUN = `${Date.now()}`;
let seq = 0;
function key() {
  seq += 1;
  return `load-${RUN}-${__VU}-${seq}`;
}

function deposit(accountID, amount) {
  return http.post(
    `${BASE}/v1/admin/deposits`,
    JSON.stringify({ account_id: accountID, amount: amount, currency: 'BRL' }),
    {
      headers: { ...JSON_HEADERS, Authorization: `Bearer ${ADMIN_TOKEN}`, 'Idempotency-Key': key() },
      tags: { name: 'deposit' },
    },
  );
}

export function setup() {
  const ids = [];
  for (let i = 0; i < ACCOUNTS; i++) {
    const res = http.post(`${BASE}/v1/accounts`, JSON.stringify({ owner_id: `load-${i}`, currency: 'BRL' }), {
      headers: JSON_HEADERS,
      tags: { name: 'setup' },
    });
    if (res.status !== 201) fail(`create account: ${res.status} ${res.body}`);
    const id = res.json('id');
    const dep = deposit(id, INITIAL_BALANCE);
    if (dep.status !== 201) fail(`initial deposit: ${dep.status} ${dep.body}`);
    ids.push(id);
  }
  return { ids };
}

function isReplay(res) {
  return res.status === 200 && res.headers['Idempotent-Replayed'] === 'true';
}

function countFailure(op, res) {
  unexpected.add(1, { op: op, status: String(res.status) });
  if (res.status === 503) serviceBusy.add(1);
  else if (res.status >= 500 || res.status === 0) serverErrors.add(1);
}

function pick(ids) {
  return ids[Math.floor(Math.random() * ids.length)];
}

function doTransfer(ids) {
  const from = pick(ids);
  let to = pick(ids);
  while (to === from) to = pick(ids);
  const amount = 1 + Math.floor(Math.random() * 1000);

  const res = http.post(
    `${BASE}/v1/transfers`,
    JSON.stringify({ from_account_id: from, to_account_id: to, amount: amount, currency: 'BRL' }),
    { headers: { ...JSON_HEADERS, 'Idempotency-Key': key() }, tags: { name: 'transfer' } },
  );
  transferDuration.add(res.timings.duration);

  if (res.status === 201) {
    transfersOK.add(1);
  } else if (isReplay(res)) {
    replays.add(1);
  } else if (res.status === 422 && res.json('error.code') === 'INSUFFICIENT_FUNDS') {
    insufficientFunds.add(1);
  } else {
    countFailure('transfer', res);
  }
  check(res, { 'transfer: expected response': (r) => r.status === 201 || r.status === 422 || isReplay(r) });
}

function doDeposit(ids) {
  const res = deposit(pick(ids), 100 + Math.floor(Math.random() * 10000));
  depositDuration.add(res.timings.duration);
  if (res.status === 201) {
    depositsOK.add(1);
  } else if (isReplay(res)) {
    replays.add(1);
  } else {
    countFailure('deposit', res);
  }
  check(res, { 'deposit: expected response': (r) => r.status === 201 || isReplay(r) });
}

export default function (data) {
  switch (SCENARIO) {
    case 'transfers':
      doTransfer(data.ids);
      break;
    case 'deposits':
      doDeposit(data.ids);
      break;
    default:
      if (Math.random() < 0.9) doTransfer(data.ids);
      else doDeposit(data.ids);
  }
}
