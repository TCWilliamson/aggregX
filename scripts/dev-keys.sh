#!/bin/sh
# Generate DEVELOPMENT keys and certificates (spec 03 §9). Never use outside dev.
# Usage: dev-keys.sh <output-dir>. Idempotent: does nothing if keys exist.
set -eu
OUT=${1:-./certs}
mkdir -p "$OUT"
cd "$OUT"
[ -f ingress-ca.pem ] && { echo "dev keys already exist in $OUT"; exit 0; }

ec() { openssl ecparam -name prime256v1 -genkey -noout -out "$1"; }

# Device-ingress CA (signs ingest's TLS server certificate; pinned by devices).
ec ingress-ca.key
openssl req -x509 -new -key ingress-ca.key -sha256 -days 3650 -subj "/CN=iotp dev device-ingress CA" -out ingress-ca.pem

# Ingest server certificate for HAProxy.
ec ingest.key
openssl req -new -key ingest.key -subj "/CN=ingest.uk1.iotp.test" -out ingest.csr
printf "subjectAltName=DNS:ingest.uk1.iotp.test,DNS:haproxy,DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n" > ingest.ext
openssl x509 -req -in ingest.csr -CA ingress-ca.pem -CAkey ingress-ca.key -CAcreateserial -days 825 -sha256 -extfile ingest.ext -out ingest.pem
cat ingest.pem ingest.key > haproxy-ingest.pem

# Gateway CA and one dev gateway certificate (spec 03 §5).
ec gateway-ca.key
openssl req -x509 -new -key gateway-ca.key -sha256 -days 3650 -subj "/CN=iotp dev gateway CA uk1" -out gateway-ca.pem
ec gw-GW-00001.key
openssl req -new -key gw-GW-00001.key -subj "/CN=GW-00001" -out gw.csr
printf "subjectAltName=URI:urn:iotp:uk1:gw:GW-00001\nextendedKeyUsage=clientAuth\n" > gw.ext
openssl x509 -req -in gw.csr -CA gateway-ca.pem -CAkey gateway-ca.key -CAcreateserial -days 90 -sha256 -extfile gw.ext -out gw-GW-00001.pem

# Online signing keys: device tokens (JWT) and cloud replies.
ec token-signing.key
ec reply-signing.key

rm -f ./*.csr ./*.ext ./*.srl
chmod 644 ./*
echo "dev keys written to $OUT"
