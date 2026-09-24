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

## Operazioni sullo stato di commit (`state`)

Task tipici quando un agente deve gestire/verificare lo stato di commit cross-host
(export `.status` serviti da `nbd-export-state.service` su porta 10819).

### Verificare se lo state di un export è esportato

```bash
# dal repo server, come root
./nbd-export.sh state show <name>       # fallisce se il file non esiste
systemctl is-active nbd-export-state.service   # serve il server di stato (10819)
ls /var/lib/launch-nbd/state/           # deve contenere <name>.status (4 KiB)
# lettura live via NBD (non consuma la slot limit=1 dell'export dati):
nbdinfo nbd://HOST:10819/<name>.status
```

Su questa macchina di sviluppo `nbdinfo` non è nel PATH di sistema: usare
`export PATH="$HOME/opt/libnbd-tools/usr/bin:$PATH"` (la produzione ha i tool di sistema).

### Creare lo state (dopo un export dati)

**Automatico**: `export <path>` crea `<name>.status` e, se il server di stato non è
attivo, lo avvia (auto-init, modello A: unit `nbd-export-state.service` enable al
boot). Opt-out con `--no-state` / `--no-status`. `remove <name>` rimuove anche il
`.status` (silenzioso; il server resta attivo). `state init` resta per configurazione
esplicita (dir/porta/TLS custom).

```bash
./nbd-export.sh export <path> --name <name>     # crea anche <name>.status (clean)
./nbd-export.sh list                            # tabella export dati + riga singola 'state'
./nbd-export.sh state show <name>               # atteso: state: clean
./nbd-export.sh remove <name>                   # unit dati + <name>.status via
```

### Invarianti da rispettare (vedi docs/plan-state-export.md)

- Il record è **sempre 4 KiB** (`truncate`); mai `rm` del file mentre l'unit state è
  attiva — usare `state remove <name>` (elimina il solo `.status`, export dati intatto).
  `remove <name>` (dati) elimina unit E `.status` insieme, silenziosamente.
- `state` è un **nome di export riservato** (`reserved_name_check`); in `list` l'unit
  state compare come riga singola `state` (porta dir), i dettagli con `status state`.
- `state set` lato server è solo amministrativo/test: il percorso normale è la scrittura
  via NBD da parte del client launcher.
- Lo stato NON è un lock: `committing` è un marker; il fail-stop è dei client che lo
  leggono (spec `docs/status-format.md`).
- La dir servita dall'unit di stato è la **fonte di verità** per `export`/`remove`
  (non la costante `STATE_DIR`): i custom `state init --dir` restano coerenti.