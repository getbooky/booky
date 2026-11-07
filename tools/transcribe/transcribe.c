// booky-transcribe: speech to timestamped words, for lining an audiobook up
// with its ebook.
//
// Reads 16 kHz mono signed 16-bit little-endian PCM on stdin (Booky pipes it
// from ffmpeg) and writes Vosk's JSON results to stdout, one object per line:
// {"result":[{"conf":1.0,"end":1.02,"start":0.6,"word":"alice"},...],"text":"..."}
// Times are seconds from the start of the input. Diagnostics go to stderr.
//
// usage: booky-transcribe <model directory>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <vosk_api.h>

#define SAMPLE_RATE 16000.0f
#define BUF_BYTES 8000 // 0.25 s of audio per call

static void emit(const char *json) {
	// Vosk pretty-prints across lines; one line per result keeps the reader
	// on Booky's side a plain line scanner
	for (const char *p = json; *p; p++) {
		if (*p != '\n' && *p != '\r') putchar(*p);
	}
	putchar('\n');
}

int main(int argc, char **argv) {
	if (argc != 2) {
		fprintf(stderr, "usage: %s <model directory>\n", argv[0]);
		return 2;
	}
	vosk_set_log_level(-1);
	VoskModel *model = vosk_model_new(argv[1]);
	if (!model) {
		fprintf(stderr, "booky-transcribe: cannot load model %s\n", argv[1]);
		return 1;
	}
	VoskRecognizer *rec = vosk_recognizer_new(model, SAMPLE_RATE);
	if (!rec) {
		fprintf(stderr, "booky-transcribe: cannot create recognizer\n");
		vosk_model_free(model);
		return 1;
	}
	vosk_recognizer_set_words(rec, 1);

	char buf[BUF_BYTES];
	size_t n;
	int pending = 0; // a lone byte carried over so samples stay 2-byte aligned
	while ((n = fread(buf + pending, 1, sizeof buf - pending, stdin)) > 0) {
		size_t total = n + pending;
		size_t even = total & ~(size_t)1;
		if (vosk_recognizer_accept_waveform(rec, buf, (int)even) == 1) {
			emit(vosk_recognizer_result(rec));
		}
		pending = (int)(total - even);
		if (pending) buf[0] = buf[even];
	}
	if (ferror(stdin)) {
		fprintf(stderr, "booky-transcribe: read error\n");
	}
	emit(vosk_recognizer_final_result(rec));
	fflush(stdout);

	vosk_recognizer_free(rec);
	vosk_model_free(model);
	return ferror(stdout) ? 1 : 0;
}
