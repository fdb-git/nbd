# AGENTS.md — workspace nbd

Workspace (repo git unico, root `/root/dev/nbd`) con due componenti, ognuno con
documentazione propria — partire sempre dai link sotto prima di toccare codice:

- `export-nbd/` — **server** NBD: `nbd-export.sh` (bash, unit systemd nbdkit).
  Doc agente: `export-nbd/AGENT.md`; spec storiche in `export-nbd/docs/`.
- `launch-nbd/` — **client** QEMU launcher: `launch-nbd.sh` (bash) e piano Go in
  `launch-nbd/docs/plan-go.md` (il client Go non è ancora implementato).

- `README.md` (root) = overview per umani; `TODO.md` (root) = stato del lavoro tra i
  due repo.

## Contratti cross-repo

- **Formato dei file di stato `.status`** (export di commit cross-host, porta 10819):
  unica fonte canonica = **`docs/status-format.md`**. Non duplicare la tabella in altri
  file. Un cambiamento di formato richiede: aggiornare il doc canonico E i test
  round-trip `export-nbd/tests/test-state-*.sh`; i plan doc restano storici.