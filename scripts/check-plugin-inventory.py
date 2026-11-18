# Static design data validation only; no plugin instantiation.
from pathlib import Path
import json
import os
os.chdir(Path(__file__).resolve().parent.parent)
p=Path('docs/plugin-dependencies.json');data=json.loads(p.read_text());ns=data['nodes'];by={n['id']:n for n in ns}
assert len(by)==len(ns)
review=Path('docs/plugin-inventory-review.md').read_text()
for n in ns:
 rows=[line for line in review.splitlines() if line.startswith('| `'+n['id']+'`<br>')]
 assert len(rows)==1, 'missing/duplicate inventory row: '+n['id']
 assert rows[0].split(' | ')[-1].startswith(str(len(n['deps']))+' / '), 'Deps count mismatch: '+n['id']

edges={n['id']:[t for d in n['deps'] for t in d['targets']] for n in ns}
for n in ns:
 fs=[d['field'] for d in n['deps']];assert len(fs)==len(set(fs)),n['id']
 assert all(t in by for t in edges[n['id']]),n['id']
def topo(es):
 done=set();active=[];order=[]
 def visit(x):
  assert x not in active,'cycle: '+' -> '.join(active+[x])
  if x in done:return
  active.append(x)
  for y in es[x]:visit(y)
  active.pop();done.add(x);order.append(x)
 for x in es:visit(x)
 return order
# State-step architecture constraints: no parallel generic review/check engine.
assert not {'reviews', 'checks'} & {d['field'] for d in by['delivery-commands']['deps']}
assert not {'results', 'commits', 'accepting'} & {t for d in by['flow-standard']['deps'] if d['field'] != 'states' for t in d['targets']}
assert not {'flows', 'flow-standard', 'acceptance-commands'} & set(edges['accepting'])
assert {d['field'] for d in by['results']['deps']} == {'submissions'}
assert 'accepting' in edges['acceptance-commands'] and 'flows' in edges['acceptance-commands']
order=topo(edges)
reach=set()
def walk(x):
 if x in reach:return
 reach.add(x)
 for y in edges[x]:walk(y)
walk('server');assert reach==set(by),set(by)-reach
# Verify phase I projection, explicitly omit phase-II list members, not dummy providers.
first={x for x,n in by.items() if not n['phase'].startswith('II')}
fedges={x:[y for y in edges[x] if y in first] for x in first};topo(fedges)
# Only list deps/root ownership are permitted to reference a later phase in phase-I baseline.
for x in first:
 for d in by[x]['deps']:
  if any(y not in first for y in d['targets']):assert d['interface'].startswith('[]'),(x,d)
# Hypothetical problematic reverse dependencies must be rejected.
for src,dst in [('results','delivery-commands'),('accepting','flows'),('tasks','flows')]:
 e={x:list(ys) for x,ys in edges.items()};e[src].append(dst)
 try:topo(e)
 except AssertionError:pass
 else:raise AssertionError('cycle injection not detected')
lines=['flowchart TD']
for i,n in enumerate(ns):lines.append(f'    n{i}["{n["id"]}<br>{n["kind"]}"]')
idx={n['id']:f'n{i}' for i,n in enumerate(ns)}
for n in ns:
 for d in n['deps']:
  for t in d['targets']:lines.append(f'    {idx[n["id"]]} -->|{d["field"]}| {idx[t]}')
assert Path('docs/plugin-dependencies.mmd').read_text() == '\n'.join(lines)+'\n', 'Mermaid graph differs from dependency inventory'
print(f'PASS: {len(ns)} nodes, {sum(len(v) for v in edges.values())} field-target edges, no missing refs/cycles, all root reachable')
print(f'PASS: phase-I candidate-node projection {len(first)} nodes; later phase refs only in explicit lists')
print('PASS: 3 intentional reverse dependency cycles rejected')
print('Deps >5:', ', '.join(f'{n["id"]}={len(n["deps"])}' for n in ns if len(n['deps'])>5))
print('STATIC DESIGN ONLY: no Go interface/type/build/business validation')
