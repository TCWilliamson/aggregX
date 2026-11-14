#!/usr/bin/env bash
# Validate golden vectors against spec/wire/envelope.cddl with zcbor (spec 04 §8.2).
# Valid vectors must pass; invalid_envelope_* vectors whose error is expressible in
# CDDL must fail. Requires: pip install zcbor 'cbor2<5.7'
set -euo pipefail
cd "$(dirname "$0")/.."
CDDL=spec/wire/envelope.cddl
V=spec/vectors
fail=0

check() { # file type expect(pass|fail)
  local hex
  hex=$(python3 -c 'import sys,binascii;print(binascii.hexlify(open(sys.argv[1],"rb").read()).decode())' "$1")
  if echo "$hex" | zcbor validate -c "$CDDL" -i - --input-as cborhex -t "$2" >/dev/null 2>&1; then got=pass; else got=fail; fi
  if [[ "$got" == "$3" ]]; then echo "ok    $(basename "$1") ($2: $got)"; else echo "FAIL  $(basename "$1") ($2: want $3, got $got)"; fail=1; fi
}

for f in "$V"/envelope_*.cbor; do check "$f" envelope pass; done
for f in "$V"/reply_*.cbor;    do check "$f" reply pass; done
for f in "$V"/cose_batch_valid_*.cbor; do check "$f" signed-batch pass; done

# Invalid vectors that CDDL can express (the rest are code-level checks).
for name in missing_inc missing_cfg unknown_key_18 v2 inc_zero dev_33 cfg_7_bytes tb_2 empty slot_0 slot_240 not_a_map; do
  f="$V/invalid_envelope_$name.cbor"
  [[ -f "$f" ]] || continue
  case $name in
    unknown_key_18|empty) continue ;;  # CDDL maps are open to "at least one of"; enforced in code
  esac
  check "$f" envelope fail
done
for f in "$V"/invalid_cose_*.cbor; do
  case $(basename "$f") in
    invalid_cose_sig_63.cbor|invalid_cose_alg_*|invalid_cose_extra_label.cbor|invalid_cose_unprotected.cbor|invalid_cose_wrong_tag.cbor|invalid_cose_untagged.cbor|invalid_cose_detached.cbor) check "$f" signed-batch fail ;;
  esac
done
exit $fail
