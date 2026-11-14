#!/usr/bin/env bash
# c-size-budget.sh: the device side of the wire contract must fit the smallest
# supported chip, the nRF52840 (Cortex-M4, 256 KB RAM, 1 MB flash).
# CONTRADICTIONS.md C28; spec 04 §8.4.
#
# What a device links from the generated code is the reply decoder only.
# Envelopes are encoded with zcbor's streaming API, record by record from the
# outbox (spec 04 §8.4), so no generated envelope struct is ever in RAM.
#
# Fails if the decoder's flash or the decoded reply's RAM exceeds its budget.
set -euo pipefail
cd "$(dirname "$0")/.."

# Budgets (bytes). Raising one is a contract change: say why in the PR.
MAX_FLASH=${MAX_FLASH:-16384}   # reply decoder + zcbor runtime, -Os, Thumb-2
MAX_REPLY_RAM=${MAX_REPLY_RAM:-8192} # one decoded struct reply (signed wrapper + payload)

CC=${CC_ARM:-arm-none-eabi-gcc}
BASE=(-mcpu=cortex-m4 -mthumb -Os -std=c11 -Wall -ffunction-sections -fdata-sections -fno-common)
CFLAGS=("${BASE[@]}" -Werror)
Z=$(python3 -c 'import zcbor,os;print(os.path.dirname(zcbor.__file__))')
OUT=build/c-size
rm -rf "$OUT"; mkdir -p "$OUT"

sed -e "s/1\*1000/1*${DEV_MAX_ITEMS:-32}/g" -e "s/1\*4096/1*${DEV_MAX_BLOCK:-256}/g" \
  spec/wire/envelope.cddl > "$OUT/envelope_device.cddl"
zcbor code -c "$OUT/envelope_device.cddl" -d -t reply signed-reply --default-max-qty 16 \
  --output-c "$OUT/dev_reply.c" --output-h "$OUT/dev_reply.h" --output-h-types "$OUT/dev_reply_types.h" >/dev/null

objs=()
# zcbor's runtime type-puns floats; build it as Zephyr does (-fno-strict-aliasing)
# and without -Werror, since it is vendored third-party code.
for src in "$Z/src/zcbor_common.c" "$Z/src/zcbor_decode.c"; do
  o="$OUT/$(basename "$src" .c).o"
  "$CC" "${BASE[@]}" -w -fno-strict-aliasing -I"$Z/include" -c "$src" -o "$o"
  objs+=("$o")
done
"$CC" "${CFLAGS[@]}" -I"$Z/include" -I"$OUT" -c "$OUT/dev_reply.c" -o "$OUT/dev_reply_decode.o"
objs+=("$OUT/dev_reply_decode.o")
flash=$(arm-none-eabi-size -t "${objs[@]}" | awk 'END{print $1+$2}')

printf '#include "dev_reply_types.h"\nchar sz_reply[sizeof(struct signed_reply) + sizeof(struct reply)];\n' > "$OUT/sz.c"
"$CC" "${CFLAGS[@]}" -I"$Z/include" -I"$OUT" -c "$OUT/sz.c" -o "$OUT/sz.o"
ram=$(arm-none-eabi-nm -S -t d "$OUT/sz.o" | awk '$4=="sz_reply"{print $2+0}')

fail=0
check() { # name value max
  if [ "$2" -gt "$3" ]; then echo "FAIL $1: $2 bytes > budget $3"; fail=1
  else echo "ok   $1: $2 bytes (budget $3)"; fi
}
check "reply decoder flash (nRF52840)" "$flash" "$MAX_FLASH"
check "decoded signed reply RAM (nRF52840)" "$ram" "$MAX_REPLY_RAM"
exit $fail
