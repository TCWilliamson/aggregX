#!/usr/bin/env bash
# Generate the C codec from the CDDL with zcbor and run the golden vectors through it.
set -euo pipefail
cd "$(dirname "$0")/.."
Z=$(python3 -c 'import zcbor,os;print(os.path.dirname(zcbor.__file__))')
OUT=sdk/c/generated
mkdir -p "$OUT" build
# Device profile: the cloud limits (1000 items per section, 4096 block values)
# make zcbor's fixed-size C structs enormous. Firmware is generated from a copy
# with device-sized limits (spec 04 §8.4).
sed -e "s/1\*1000/1*${DEV_MAX_ITEMS:-32}/g" -e "s/1\*4096/1*${DEV_MAX_BLOCK:-256}/g" \
  spec/wire/envelope.cddl > "$OUT/envelope_device.cddl"
zcbor code -c "$OUT/envelope_device.cddl" -e -d \
  -t envelope reply signed-batch signed-reply relay-request relay-reply \
  --default-max-qty 16 \
  --output-c "$OUT/iotp_wire.c" --output-h "$OUT/iotp_wire.h" --output-h-types "$OUT/iotp_wire_types.h"
gcc -std=c11 -Wall -O1 -I"$Z/include" -I"$OUT" \
  "$Z/src/zcbor_common.c" "$Z/src/zcbor_decode.c" "$Z/src/zcbor_encode.c" \
  "$OUT/iotp_wire_decode.c" "$OUT/iotp_wire_encode.c" sdk/c/test/vectors_test.c \
  -o build/c_vectors_test
./build/c_vectors_test spec/vectors
