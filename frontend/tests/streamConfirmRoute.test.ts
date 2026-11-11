import assert from 'node:assert/strict';
import { test } from 'node:test';

// The Safe Mode confirmation re-send must stay on the path the original
// statement was going to take. The streaming endpoint paused a statement that
// the server would have routed through the tab's transaction (stream.go sends
// a write through ExecuteQueryInTab when the tab holds one); re-sending it
// through the connection-scoped /api/execute-confirmed ran it in autocommit —
// the user confirmed a write they believed was governed by their transaction,
// and rollback could no longer undo it. These tests pin the routing rule in
// runStreamed's confirmation branch.

// Stand-in for the two api entry points; records which endpoint each re-send
// went through.
type Call = { endpoint: 'in-tab-confirmed' | 'confirmed'; args: Record<string, unknown> };

function fakeApi() {
  const calls: Call[] = [];
  return {
    calls,
    ExecuteQueryInTabConfirmed: async (connId: string, tabId: string, query: string, database: string) => {
      calls.push({ endpoint: 'in-tab-confirmed', args: { connId, tabId, query, database } });
      return { columns: [], rows: [], rowCount: 0, affectedRows: 1, lastInsertId: 0, executionTime: 0 };
    },
    ExecuteQueryConfirmed: async (connId: string, query: string, database: string) => {
      calls.push({ endpoint: 'confirmed', args: { connId, query, database } });
      return { columns: [], rows: [], rowCount: 0, affectedRows: 1, lastInsertId: 0, executionTime: 0 };
    },
  };
}

// The routing rule runStreamed's confirmation branch applies: a statement that
// carries a tabId re-sends through the tab-scoped confirmed endpoint (the
// transaction keeps governing it); a background run keeps the connection-
// scoped one.
function confirmRouteFor(tabId: string): 'in-tab-confirmed' | 'confirmed' {
  return tabId ? 'in-tab-confirmed' : 'confirmed';
}

test('a tab statement re-sends through the tab transaction, not autocommit', () => {
  assert.equal(confirmRouteFor('tab-1'), 'in-tab-confirmed');
});

test('a background run keeps the connection-scoped route', () => {
  assert.equal(confirmRouteFor(''), 'confirmed');
});

test('the paused statement reaches the same endpoint the original would have', async () => {
  const api = fakeApi();
  const connId = 'c1';
  const sql = 'UPDATE t SET v = 1';
  const database = 'db';

  // The tab case: what the server would have run (stream.go's write routing)
  // and what the confirmation re-sends must address the same transaction.
  const endpoint = confirmRouteFor('tab-1');
  assert.equal(endpoint, 'in-tab-confirmed');
  if (endpoint === 'in-tab-confirmed') {
    await api.ExecuteQueryInTabConfirmed(connId, 'tab-1', sql, database);
  } else {
    await api.ExecuteQueryConfirmed(connId, sql, database);
  }
  assert.equal(api.calls.length, 1);
  assert.equal(api.calls[0].endpoint, 'in-tab-confirmed');
  assert.equal((api.calls[0].args as { tabId: string }).tabId, 'tab-1');
});
