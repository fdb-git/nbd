# Pianificazione modifiche lato server nbdkit — export di stato per commit interrotto

**Stato:** pianificazione (nessun codice modificato in questa sessione). Documento di lavoro per estendere `nbd-export.sh` e il suo ambiente systemd; il contratto client-side è nel piano Go `launch-nbd/docs/plan-go.md` §5.10.

---

## 1. Contesto / problema

Un overlay qcow2 di un'installazione può essere committato sull'export NBD (`qemu-img commit`). Se il processo è **interrotto** (kill, crash dell'host client, rete), il disco resta in stato ibrido: parte dei cluster espulsi, il resto ancora nell'overlay. Avviare un host qualsiasi su quel disco = corruzione.

La difesa già progettata lato client (`launch-nbd`) è il marker `-committing` **nel filename** dell'overlay (§5.8): è host-locale. Un client su un **altro host** (stesso export NBD) non la vede.

**Soluzione (opzione 1, decisa):** esporre lo stato di commit come **export NBD dedicato** servito dal plugin `file` di nbdkit su un file **persistente su disco** (sopravvive a riavvii e crash; a differenza di `memory`/`tmpdisk` che sono volatili). Il launcher client, prima di toccare il disco, legge il marker e si ferma se `committing`; durante `commit` scrive `committing` (+`FLUSH`) prima e `clean` (+`FLUSH`) dopo.

---

## 2. Decisioni prese

| Asse | Decisione |
|---|---|
| Topologia | **un solo server di stato per host**: unit `nbd-export-state.service` che serve tutti i file `<export>.status` con `nbdkit file dir=<STATE_DIR>` |
| Porta | **dedicata fissa**: `STATE_PORT`, default `10819` |
| Formato file | **binario fisso 4 KiB**, creato con `truncate` all'init |
| Filtri | l'unit di stato **NON** porta `--filter=limit` / `--filter=multi-conn` (restano solo sull'unit dati): più client leggono lo stato in concorrenza senza consumare lo slot `limit=1` |
| Persistenza | plugin `file` su regular file → scritta dischi sincrona, sopravvive a riavvii/crash; durabilità degli aggiornamenti garantita dal client con **`NBD_CMD_FLUSH`** dopo la scrittura |

---

## 3. Contratto export di stato

- **Export name:** `<name>.status` (nome dati + suffisso `.status`), es. `demo.status`.
- **URI:** `nbd://<addr>:<STATE_PORT>/<name>.status`.
- **Accesso:** il plugin `file` con `dir=` mappa l'export name sul file `$STATE_DIR/<name>.status`. Serve solo la **selezione per nome** lato client (funziona anche su nbdkit 1.36.3); l'**advertising** multi-export (lista export) è di nbdkit ≥1.38 — da trattare come best-effort, non requisito.
- **Dimensione:** sempre 4 KiB (file regolare inizializzato con `truncate`; la size esposta da nbdkit = stat del file).
- **Scrittura:** singolo blocco da 4 KiB zero-padded (nessun parsing "a lunghezze variabili" fragile); il client invia `NBD_CMD_FLUSH` dopo la scrittura del marker.
- **Lettura manuale:** `nbdinfo nbd://HOST:10819/<name>.status` è ammesso — la lettura dell'export di stato **non** tocca il limite `limit=1` dell'export dati.

### Formato del file (4 KiB)

**Storico — contratto canonico: `docs/status-format.md` (root del workspace).**
La tabella originale di questa sezione vive ora nel doc root (corretta per l'escape del
magic: `NBDST\0\0\0` a offset 0–7, version a offset 8) e nei test round-trip
`tests/test-state-*.sh`; non duplicarla qui.

Note (semantica, invariata):
- `committed` = commit completato ma marker non ancora azzerato (`clean`), per diagnosi.
- Lo stato **non** è un lock: è un marker. Il fail-stop è responsabilità dei client che lo leggono.

---

## 4. Modifiche a `nbd-export.sh`

### 4.1 Variabili `.env` (default in script)

- `STATE_DIR` default `/var/lib/launch-nbd/state` (dir contenente i file `<name>.status`)
- `STATE_PORT` default `10819`
- `STATE_ADDR` default `0.0.0.0`

### 4.2 Nuovo subcommand `state` (evita la collisione col `status` esistente che ispeziona le unit)

```
nbd-export.sh state init [--dir DIR] [--port N] [--addr IP] [--force]
    crea $DIR, genera l'unit nbd-export-state.service e la avvia
nbd-export.sh state export <name> [--dir DIR]
    garantisce l'esistenza di $DIR/<name>.status (truncate 4K, magic NBDST,
    stato=clean, owner=hostname/UUID) senza alterare gli export dati esistenti
nbd-export.sh state set <name> <clean|committing|committed> [--dir DIR]
    scrive direttamente il marker (percorso server-admin/test; il percorso
    normale è la scrittura via NBD del client launcher)
nbd-export.sh state show <name> [--dir DIR]
    dump interpretato dei campi (magic, stato, owner, hash, ctime)
nbd-export.sh state remove <name> [--dir DIR]
    elimina il file di stato (e, se vuota, la dir); l'unit state resta per gli altri export
```

Comportamento di default: dopo `export <path>` l'export di stato non è creato automaticamente (esplicito con `state export <name>`), così nessuna modifica retroattiva al flusso dati esistente. Opzione da valutare: `--with-state` in `cmd_export` per creare più i file insieme.

### 4.3 Unit `nbd-export-state.service` (stile `write_unit`)

```
ExecStart=/usr/bin/nbdkit --foreground file dir=<STATE_DIR> \
    --port=<STATE_PORT> --ipaddr=<STATE_ADDR>
```

- **nessun** `--filter=limit` / `--filter=multi-conn` / `--readonly` (deve accettare le scritture dati);
- `--user/--group` = proprietario di `STATE_DIR` (stesso modello "runs as owner" dei file export);
- hardening copiato da `write_unit`: `NoNewPrivileges=yes`, `PrivateTmp=yes`, `ProtectHome=yes`, `ProtectSystem=full`, `RestrictAddressFamilies=AF_INET AF_UNIX`; `Restart=on-failure`;
- `After=network-online.target` + `Wants=network-online.target`;
- unit unica: `UNIT_PREFIX-state` → `nbd-export-state.service`; il `cmd_list`/`cmd_status` attuali lo tratteranno come un export "normale": verificare che `extract_image`/tabelle lo espongano senza confondersi (es. mostrarlo o escluderlo esplicitamente).

### 4.4 TLS

Stessi certificati del resto (`--tls=…`, `--tls-certificates=$TLS_DIR_DEFAULT`): il client launcher parla cifrato col server di stato, **ma** il marker resta scritto dal client di commit sulla stessa porzione di rete. Opzione futura: `--allow CIDR` ristretto o `--filter=ip` sull'unit di stato.

---

## 5. Verifiche tecniche (nbdkit 1.36.3)

Da confermare sul sistema target prima di implementare:

- [x] `nbdkit file dir=<DIR>`: selezione per nome (`/name.status`) funzionante con 1.36.3 (senza list multi-export) — verificato su nbdkit 1.36.3 (build user-space): `nbdsh demo.status → size 4096`;
- [x] `flush`/`fua` attivi per regular file (il client usa `NBD_CMD_FLUSH` per la durabilità) — `can_flush=True`;
- [x] comportamento con file < 4 KiB se rimossi/ricreati (restare sempre a 4 KiB via `truncate`, mai `rm` durante il run) — size esposta = stat del file (100 su file troncato a 100); il piano ripristina sempre 4096;
- [x] `stat`/size esposta = 4096 (file 4 KiB); eventuale `--threads` non richiesto;
- [ ] `cmd_list`/`cmd_status` con la nuova unit `state`: nessuna regressione a `extract_image` e `limit` parser — verificato nel Task 6 del piano (`tests/test-state-list-status.sh`).

---

## 6. Interazione col client launcher (`launch-nbd` §5.10)

- il launcher legge `<export>.status` **prima** di `run`/`direct`/`--snapshot`/`install`; `state=committing` → blocca con messaggio di ripresa;
- `commit` scrive `committing`+`FLUSH` → `qemu-img commit` → `clean`+`FLUSH`;
- la scrittura avviene via **NBD** dal client (percorso normale), `state set` lato server è solo amministrativo/test — i due percorsi convergono sullo stesso formato su disco.

---

## 7. Test / verifica (da integrare nella procedura AGENT §6 e in `tests/`)

1. `state init` + `state export <name>` → file 4 KiB con magic corretto; unit attiva e in ascolto su `10819`.
2. `state show` ↔ contenuto atteso (clean dopo export).
3. Scrittura via NBD (mini client Python) di `committing` + `FLUSH`, poi riavvio di `nbd-export-state.service` → il file conserva `committing` (persistenza).
4. `nbdinfo nbd://HOST:10819/<name>.status` funziona e non tocca il limite `limit=1` dei dati.
5. `state set <name> clean` → round-trip del campo stato.
6. `state remove <name>` → file e relativi riferimenti in unit rimossi, export dati intatti.

---

## 8. Open questions / fasi

> Stato v1 (2026-09-19, opzione A confermata dall'utente): le tre open question qui
> sotto restano escluse dal v1 e sono conservate come backlog. Il piano di
> implementazione: `docs/superpowers/plans/2026-09-19-state-export.md`.

- [ ] Rendere lo stato leggibile/scrivibile in modo sicuro da client non fidati (`--filter=ip`, `--filter=exportname`, o rotazione del marker) — fuori scope v1.
- [ ] Integrare `--with-state` in `cmd_export` (creare il file `.status` insieme all'export dati) come comodità.
- [ ] Advertising multi-export (nbdkit ≥1.38) per il discovery, se un domani l'host dati viene aggiornato.

---

Le verifiche §5 sopra sono
state eseguite con nbdkit 1.36.3 (build user-space in `~/opt/nbdkit/sbin`) e
`nbdsh`/`nbdinfo` 1.10.5 estratti in `~/opt/libnbd-tools/usr/bin` (nessuna
modifica ai pacchetti di sistema); la macchina di prova ha apt nbdkit 1.24.1.

## 9. Tooling di verifica "in home" (nessuna modifica di sistema)

Sulla macchina di sviluppo (aarch64, jammy) apt offre solo nbdkit 1.24.1 e
libnbd-bin 1.10.5, ma i tool client NON erano installati e la spec richiede
di verificare su 1.36.3. Setup ripetibile (tutto in `$HOME`, nessun dpkg):

```bash
# nbdkit 1.36.3 -> ~/opt/nbdkit (/sbin/nbdkit)
mkdir -p ~/src && cd ~/src
curl -sO https://download.libguestfs.org/nbdkit/1.36-stable/nbdkit-1.36.3.tar.gz
tar xf nbdkit-1.36.3.tar.gz && cd nbdkit-1.36.3
./configure --prefix="$HOME/opt/nbdkit" --disable-gnutls
make -j"$(nproc)" && make install

# libnbd tools 1.10.5 (nbdsh/nbdinfo/nbdcopy) + binding python, estratti da .deb
cd ~/src && apt-get download libnbd-bin python3-libnbd
dpkg-deb -x libnbd-bin_*.deb ~/opt/libnbd-tools
dpkg-deb -x python3-libnbd_*.deb ~/opt/libnbd-tools

# ambiente per i test
export PATH="$HOME/opt/nbdkit/sbin:$HOME/opt/libnbd-tools/usr/bin:$PATH"
export PYTHONPATH="$HOME/opt/libnbd-tools/usr/lib/python3/dist-packages"
```

Su produzione (nbdkit 1.36.3 di sistema + libnbd tools) le stesse verifiche
si eseguono senza PATH/PYTHONPATH custom.