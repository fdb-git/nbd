#!/usr/bin/env bash
#
# Test: build_exec() deve applicare --filter=multi-conn SOLO se il filtro
# esiste in nbdkit (introdotto in versioni recenti; nbdkit 1.24/jammy non
# lo ha -> senza questa protezione il servizio va in restart loop).
# Senza filtro, nbdkit di default NON annuncia multi-conn = comportamento
# sicuro equivalente a multi-conn-mode=disable.
#
set -u

NBD_EXPORT_SOURCED=1
. "$(dirname "$0")/../nbd-export.sh"

D="$(mktemp -d)"
mkdir -p "$D" && touch "$D/nbdkit-multi-conn-filter.so"

fail=0

run_build() {  # crea un filtro finto in $D? 1=si
    local with_filter="$1"
    if [ "$with_filter" = 1 ]; then
        NBDKIT_FILTER_DIR="$D"
    else
        NBDKIT_FILTER_DIR="$D/empty"
    fi
    # globali usate da build_exec (default script)
    opt_log=0; opt_exit_last=0; opt_allow=""; opt_limit=1; opt_luks=""
    opt_readonly=0; opt_multi_conn=0; opt_threads=""
    tls_mode=off; tls_dir=/etc/pki/nbdkit; opt_psk=""
    user="root"; group=""
    build_exec /dev/sdc3 sdc3 10809 0.0.0.0
    BUILD_OUT="$EXEC"
}

echo "== filtro multi-conn PRESENTE -> usato =="
run_build 1
echo "$BUILD_OUT" | grep -q -- '--filter=multi-conn' && echo "ok: --filter=multi-conn presente" || { echo "FAIL: --filter=multi-conn mancante"; fail=1; }
echo "$BUILD_OUT" | grep -q 'multi-conn-mode=disable' && echo "ok: multi-conn-mode=disable presente" || { echo "FAIL: multi-conn-mode mancante"; fail=1; }

echo "== filtro multi-conn ASSENTE -> omesso, nessun crash =="
run_build 0
echo "$BUILD_OUT" | grep -q -- '--filter=multi-conn' && { echo "FAIL: --filter=multi-conn presente senza filtro"; fail=1; } || echo "ok: --filter=multi-conn omesso"
echo "$BUILD_OUT" | grep -q 'multi-conn-mode=' && { echo "FAIL: multi-conn-mode presente senza filtro"; fail=1; } || echo "ok: nessun multi-conn-mode"

echo "== MULTI_CONN esplicito + filtro assente -> warning ma niente crash (safe default) =="
run_build 0
opt_multi_conn=1
build_exec /dev/sdc3 sdc3 10809 0.0.0.0
BUILD_OUT="$EXEC"
echo "$BUILD_OUT" | grep -q 'multi-conn-mode=' && { echo "FAIL: multi-conn-mode presente senza filtro (richiesto --multi-conn)"; fail=1; } || echo "ok: omesso anche con --multi-conn esplicito"

echo "== NBDKIT_FILTER_DIR non settata (set -u) -> nessun crash =="
unset NBDKIT_FILTER_DIR
opt_multi_conn=0
if build_exec /dev/sdc3 sdc3 10809 0.0.0.0 2>/dev/null; then
    echo "ok: build_exec non fallisce con variabile non settata"
else
    echo "FAIL: build_exec fallisce (unbound variable o errore)"; fail=1
fi

rm -rf "$D"
exit $fail