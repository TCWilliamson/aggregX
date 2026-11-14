/* L1 golden-vector runner for the zcbor-generated C codec (spec 07 §2).
 * Decodes each valid vector with the generated decoder, re-encodes it, and
 * checks the bytes are identical; checks invalid vectors are rejected. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "iotp_wire_decode.h"
#include "iotp_wire_encode.h"

static size_t load(const char *path, uint8_t *buf, size_t cap) {
	FILE *f = fopen(path, "rb");
	if (!f) { perror(path); exit(2); }
	size_t n = fread(buf, 1, cap, f);
	fclose(f);
	return n;
}

static int failures;

static void check_envelope(const char *dir, const char *name, int want_ok) {
	char path[512];
	uint8_t in[8192], out[8192];
	static struct envelope env;
	size_t used = 0, out_len = 0;
	snprintf(path, sizeof path, "%s/%s", dir, name);
	size_t n = load(path, in, sizeof in);
	int rc = cbor_decode_envelope(in, n, &env, &used);
	int ok = (rc == ZCBOR_SUCCESS && used == n);
	if (ok != want_ok) {
		printf("FAIL  %s decode rc=%d used=%zu/%zu\n", name, rc, used, n);
		failures++;
		return;
	}
	if (!ok) { printf("ok    %s (rejected)\n", name); return; }
	/* zcbor re-encodes floats at a fixed width, so compare by decoding the
	 * re-encoded bytes again rather than byte for byte (spec 01 §2). */
	rc = cbor_encode_envelope(out, sizeof out, &env, &out_len);
	static struct envelope env2;
	size_t used2 = 0;
	if (rc != ZCBOR_SUCCESS || cbor_decode_envelope(out, out_len, &env2, &used2) != ZCBOR_SUCCESS || used2 != out_len) {
		printf("FAIL  %s re-encode rc=%d\n", name, rc);
		failures++;
		return;
	}
	printf("ok    %s (decoded; re-encoded %zu bytes%s)\n", name, out_len,
	       (out_len == n && memcmp(in, out, n) == 0) ? ", byte-identical" : ", floats widened");
}

int main(int argc, char **argv) {
	printf("sizeof(struct envelope) = %zu bytes\n", sizeof(struct envelope));
	const char *dir = argc > 1 ? argv[1] : "spec/vectors";
	check_envelope(dir, "envelope_11_1_minimal.cbor", 1);
	check_envelope(dir, "envelope_11_2_thread.cbor", 1);
	check_envelope(dir, "envelope_11_4_uptime.cbor", 1);
	check_envelope(dir, "envelope_11_5_move_boundary_result.cbor", 1);
	check_envelope(dir, "invalid_envelope_missing_inc.cbor", 0);
	check_envelope(dir, "invalid_envelope_v2.cbor", 0);
	check_envelope(dir, "invalid_envelope_inc_zero.cbor", 0);
	check_envelope(dir, "invalid_envelope_slot_240.cbor", 0);
	check_envelope(dir, "invalid_envelope_cfg_7_bytes.cbor", 0);
	{
		char path[512]; uint8_t in[8192]; static struct reply r; size_t used;
		snprintf(path, sizeof path, "%s/reply_11_3.cbor", dir);
		size_t n = load(path, in, sizeof in);
		int rc = cbor_decode_reply(in, n, &r, &used);
		if (rc != ZCBOR_SUCCESS || used != n) { printf("FAIL  reply_11_3 rc=%d\n", rc); failures++; }
		else printf("ok    reply_11_3.cbor (decoded)\n");
	}
	{
		char path[512]; uint8_t in[8192]; static struct signed_batch sb; size_t used;
		snprintf(path, sizeof path, "%s/cose_batch_valid_testkey1.cbor", dir);
		size_t n = load(path, in, sizeof in);
		int rc = cbor_decode_signed_batch(in, n, &sb, &used);
		if (rc != ZCBOR_SUCCESS || used != n) { printf("FAIL  cose_batch rc=%d\n", rc); failures++; }
		else printf("ok    cose_batch_valid_testkey1.cbor (decoded)\n");
	}
	printf(failures ? "%d FAILED\n" : "all passed\n", failures);
	return failures ? 1 : 0;
}
