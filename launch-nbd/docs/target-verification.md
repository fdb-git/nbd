# Verifica end-to-end sul target (client Go ↔ nbdkit reale)

Procedura per validare il client Go (`launch-nbd`) contro il server nbdkit di
produzione. Complementa `docs/build-toolchain.md` (build per host) e
`docs/plan-go.md` (piano/deciso). La parte server è in `export-nbd/AGENT.md`.

## Risultati (2026-10-08, target Odroid 192.168.1.112)

Eseguito `bash ./tests/target-e2e.sh` (root, Go 1.26.8 linux/arm64) su
`fdbhome` (block, 10809, limit=1) + state export 10819:

| Passo | Esito |
|---|---|
| prerequisiti (go, nbd-export.sh, state unit, porta 10819) | OK |
| suite unit (`go test ./...`) sul target | OK |
| `TestE2EStateRoundTrip` (stato reale + FLUSH) | **PASS** |
| `TestE2EFingerprint` (fingerprint stabile del disco) | **PASS** |
| `TestE2ECommitLifecycle` (commit / interruzione / ripresa) | **PASS** |
| dry-run (fingerprint + overlay reale via `qemu-img` + args) | **OK** (dopo `apt-get install qemu-utils`) |
| probe `nbdinfo` sull'export dati | **OK** (dopo `apt-get install libnbd-bin`) |
| cleanup (`e2e-state-*` rimossi, `fdbhome` clean) | OK |

Esito complessivo: **RISULTATO: TUTTO OK** (deterministico, più corse). Il
fingerprint del disco reale è `XgOA5AXVmbegAqWIsq5Z8Hm30xj7PupJLKURRULQeVA`;
l'overlay creato è `<overlay_dir>/<hash>.qcow2` con backing
`nbd://127.0.0.1:10809/fdbhome`; `nbdinfo --size` conferma disco ~484 GiB e
state export a 4096 byte.

**Tre bug reali di prodotto trovati e corretti** in questa sessione (invisibili
col mock):

1. **`Close()` si bloccava**: inviava `NBD_CMD_DISC` attendendo una reply che il
   protocollo non prevede → hang. Fix: invio senza attesa + deadline per
   operazione. Commit `c6fd342`.
2. **`limit=1` rifiuta le connessioni ravvicinate** (`nbdkit: limit: too many
   clients connected, connection rejected`): il secondo `Fingerprint` immediato
   riceveva EOF. Fix: retry con backoff in `Dial`. Commit `feffd0d`.
3. **overlay mai creato se assente**: `GuardRun` ritorna `found=false` con
   stato zero (`OverlayLive`); lo `switch` cadeva nel ramo "riusa" → path vuoto.
   Fix: `qemu.EnsureOverlay` (crea/riusa/rinomina/blocca) + 5 test. Commit
   `0ddd136`.

E una **race del solo harness di test** (non del prodotto): i due package `e2e`
giravano in parallelo condividendo lo stesso file di stato di test → fixture
isolati per package e `-p 1` (commit `66e9b4e`).

**Note operative del target** (risolte):

- `nbd-export-fdbhome.service` era `disabled` → ora `enable fdbhome --now`
  (riparte al boot).
- `qemu-img` assente → `apt-get install qemu-utils` (6.2.0).
- `nbdinfo` non funzionante (l'estrazione in `~/opt` non includeva `libnbd0`) →
  `apt-get install libnbd-bin` (1.10.5-1, **stessa versione**, porta `libnbd0`).
  Ora `/usr/bin/nbdinfo` è di sistema; l'estrazione in `~/opt/libnbd-tools` è
  ridondante.

> `AGENTS.md` diceva «la produzione ha i tool di sistema» per `nbdinfo`: su
> questo target non era vero (nessun pacchetto libnbd installato). Ora lo è.

---

## Target di riferimento (esempio reale)

| Voce | Valore |
|---|---|
| Server nbdkit | `192.168.1.112` (Odroid; in locale `127.0.0.1`) |
| Export dati | `fdbhome` — block device `/dev/disk/by-id/ata-…-part3`, porta **10809**, **limit=1**, stato attivo |
| Export di stato | unit `nbd-export-state.service`, dir `/var/lib/launch-nbd/state`, porta **10819**, TLS **off** |
| Client | binario `launch-nbd` (Go), su questo target o su un host che lo raggiunge |

**Sicurezza dei test:** la procedura **non scrive mai sull'export dati**. Usa un
export di stato dedicato (`e2e-state`) creato/rimosso dallo script; l'export dati
è solo letto (fingerprint). Con `limit=1` nessun'altra VM/client deve essere
connesso durante i test.

## 0. Prerequisiti

Sul client (per il build): Go ≥ 1.22 (`go version`). Sul target:
`nbd-export-state.service` attiva e porte raggiungibili.

```bash
systemctl is-active nbd-export-state.service     # active
./nbd-export.sh state show fdbhome               # deve esistere (clean)
```

## 1. Build del client

```bash
# sul target (Odroid aarch64, Go installato):
cd ~/dev/nbd/launch-nbd && go build -o bin/launch-nbd ./cmd/launch-nbd

# oppure cross-build dall'host di sviluppo (Windows/Linux x86_64):
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/launch-nbd-arm64 ./cmd/launch-nbd
```

## 2. Verifica automatica (script)

```bash
# dal repo launch-nbd, come root:
sudo ./tests/target-e2e.sh
# oppure, verso il server remoto:
sudo NBD_HOST=192.168.1.112 NBD_DATA_EXPORT=fdbhome ./tests/target-e2e.sh
```

Lo script esegue, con esito PASS/FAIL per passo:

1. **Prerequisiti** — `go`, state unit attiva, porte 10809/10819 raggiungibili.
2. **Crea/rimuove** il file `<NBD_TEST_EXPORT>.status` (default `e2e-state`).
3. **Suite unit** — `go test ./...` (tutti i package).
4. **e2e NBD reale** (`-tags e2e`), vedi §3.
5. **Dry-run** (opzionale, `SKIP_DRYRUN=1` per saltarlo) — fingerprint reale +
   creazione overlay reale (qemu-img) + generazione args, con `QEMU=/bin/echo`
   (`net_mode=none`, `display_mode=none`): non boota nulla e non tocca la rete.
6. **Cleanup** — rimuove `e2e-state.status` e verifica che `fdbhome` resti `clean`.

## 3. Test e2e (cosa verificano)

`LAUNCH_NBD_E2E=1 go test -tags e2e -run E2E ./internal/nbd ./internal/qemu`
(variabili: `NBD_HOST`, `NBD_DATA_PORT`, `NBD_STATE_PORT`, `NBD_DATA_EXPORT`,
`NBD_TEST_EXPORT` — il file `.status` di `NBD_TEST_EXPORT` deve esistere).

| Test | Verifica | Atteso |
|---|---|---|
| `TestE2EStateRoundTrip` | `ReadState`/`WriteState` reali su `<test>.status`, formato `docs/status-format.md`, `NBD_CMD_FLUSH` | committing round-trip esatto; reset clean |
| `TestE2EFingerprint` | `Fingerprint` del **disco reale** (head/tail 64 KiB) | stabile tra due letture, 43 char Base64URL |
| `TestE2ECommitLifecycle` | `Commit` col fingerprint reale e stato reale; qemu-img finto | caso ok: stato `clean` + overlay `-committed`; caso interrotto: marker `-committing` + stato `committing`; `GuardRun` blocca; ripresa completa |

## 4. Controprove manuali (indipendenti dal client Go)

```bash
./nbd-export.sh state show <test>            # campo stato coerente coi test
nbdinfo nbd://127.0.0.1:10819/fdbhome.status # lettura live (NON consuma limit=1)
./nbd-export.sh state show fdbhome           # atteso: clean a fine test
```

## 5. (Opzionale, ultimo) Boot reale di una VM

È l'accettazione finale ma **non** è necessaria per la parità del client.
Richiude QEMU reale (asset popolati o `QEMU`/`QEMU_IMG` nel PATH) e **sempre**
overlay, per non scrivere sull'export:

```bash
sudo ./bin/launch-nbd run \
  --set nbd_host=192.168.1.112 --set nbd_port=10809 --set nbd_export=fdbhome \
  --set nbd_state_port=10819 --set disk_mode=overlay --set overlay_dir=/var/lib/launch-nbd/overlay
# la VM scrive in locale; il disco NBD resta intatto finché non fai 'commit'
```

Attenzione: `commit` **scrive davvero** sull'export dati — usalo solo con un
overlay di cui conosci i contenuti. Nessun'altra VM/client deve essere connesso
(limit=1).

## 6. Interpretazione dei fallimenti

| Sintomo | Causa probabile |
|---|---|
| `ReadState`/`Fingerprint`: connection refused | porta/ host errati, unit non attiva, firewall (10809/10819) |
| probe TCP grezza sulla porta dati rifiutata | **normale con `limit=1`**: il filtro limita le connessioni; la verifica autorevole è `nbdinfo` o il nostro e2e (**non** una `/dev/tcp`) |
| `nbdinfo` non trovato | export `PATH="$HOME/opt/libnbd-tools/usr/bin:$PATH"` (vedi `AGENTS.md`) |
| Fork/errore dopo `Fingerprint` | un'altra VM/client tiene lo slot `limit=1` (chiudilo) |
| `<test>.status` non esiste | manca `nbd-export.sh state export <test>` |
| `can_flush`/write di stato falliscono | TLS attivo sull'unit di stato (qui è off) o permessi owner della dir |
| commit: marker `-committing` che resta | qemu-img fallito/interrotto → ri-esegui `commit` (ripresa) |

## 7. Fuori scope di questa verifica

- Boot corretto del guest e prestazioni (richiede VM/QEMU reale).
- Layer Windows (TAP-Windows6/NAT) e accel WHPX: runtime non verificabile qui.
- Concorrenza multi-host reale sullo stesso export (il contratto §5.10 è testato
  con mock + e2e a singolo host).
