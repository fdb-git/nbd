# nbd

Progetto NBD (Network Block Device) in due parti: un server che esporta
dischi raw via `nbdkit` e un launcher QEMU che avvia una VM bootando da
quell'esportazione in rete.

```mermaid
graph LR
    subgraph server["Server (export-nbd)"]
        NBDKIT["nbdkit :10809"]
        IMG["fdbnode (raw)"]
        NBDKIT --> IMG
    end

    subgraph host["Host (launch-nbd)"]
        QEMU["QEMU"]
        VIRTIO["virtio-blk"]
        NBD0["nbd0 (blockdev NBD)"]
        QEMU --> VIRTIO --> NBD0
    end

    NBD0 -.->|NBD/TCP| NBDKIT
```

## Componenti

### `export-nbd/` — server (nbdkit, ruolo server)

Script unico `nbd-export.sh`: crea file immagine ed esporta file/block
device come servizi **systemd** (`nbd-export-<name>.service`), senza file
di configurazione (l'unità generata è lo stato). Supporta filtri (`limit`,
`multi-conn`, `luks`, `ip`, …), `--readonly`, TLS, e un modello di privilegi
per tipo di sorgente. Host di riferimento: Odroid aarch64 (Ubuntu, nbdkit 1.36).

```bash
./nbd-export.sh export disk.img --name fdbnode --port 10809
./nbd-export.sh list | status | start | stop | remove ...
```

### `launch-nbd/` — client (QEMU, repo git a sé)

`launch-nbd.sh` avvia una VM QEMU il cui disco raw vive sull'export NBD del
server (`NBD_HOST:NBD_PORT`, export `NBD_EXPORT` — di default `fdbnode`).
La connessione NBD la fa QEMU lato host: il guest vede un normale disco
`virtio-blk`, senza rete speciale nel guest. Gestisce firmware UEFI/OVMF,
ISO di installazione (download + SHA256), overlay/snapshot locali qcow2,
display (spice/gtk/vnc), rete (dual/nat/tap/bridged), audio, cache/AIO.
Configurazione in `.env` (copia di `.env.example`).

```bash
./launch-nbd.sh                 # boot dal disco NBD
./launch-nbd.sh --overlay       # overlay locale sopra l'NBD
./launch-nbd.sh --iso <file|URL>
```

## Flusso tipico

```mermaid
sequenceDiagram
    actor U as Utente
    participant S as Server (nbd-export.sh)
    participant H as Host (launch-nbd.sh)
    participant V as VM QEMU

    U->>S: export IMMAGINE --name fdbnode --port 10809
    S-->>U: unit systemd nbd-export-fdbnode attiva
    U->>H: NBD_HOST / NBD_EXPORT in .env
    U->>H: ./launch-nbd.sh
    H->>S: connessione NBD (export fdbnode)
    H->>V: avvia QEMU (virtio-blk su nbd0)
```

1. Sul server: `nbd-export.sh export <immagine> --name fdbnode --port 10809`
2. Sull'host: imposta `NBD_HOST`/`NBD_EXPORT` in `launch-nbd/.env`
3. Avvia la VM con `launch-nbd/launch-nbd.sh`
