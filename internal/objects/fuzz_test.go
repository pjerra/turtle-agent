package objects

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/testfixture"
)

// Fuzz targets for the object parsers (see internal/game/fuzz_test.go): any bytes must give
// a value or an error, never a panic or a hang. SMSG_MONSTER_MOVE has its own target,
// FuzzParseMonsterMove, in moves_test.go. The compressed packets are fuzzed twice: raw, which
// mostly exercises Inflate, and as the plain bytes of a well-formed zlib frame, which reaches
// the parsers behind it.

// inflated returns the plain bytes of each compressed seed.
func inflated(tb testing.TB, ops ...uint16) [][]byte {
	tb.Helper()
	var out [][]byte
	for _, b := range testfixture.Bodies(tb, ops...) {
		plain, err := Inflate(b)
		if err != nil {
			tb.Fatalf("seed does not inflate: %v", err)
		}
		out = append(out, plain)
	}
	return out
}

func FuzzParseUpdate(f *testing.F) {
	seeds := append(testfixture.Bodies(f, opcode.SMSGUpdateObject), inflated(f, opcode.SMSGCompressedUpdateObject)...)
	testfixture.FuzzBody(f, seeds, func(b []byte) { _, _ = ParseUpdate(b) })
}

func FuzzParseCompressedUpdate(f *testing.F) {
	testfixture.FuzzBody(f, inflated(f, opcode.SMSGCompressedUpdateObject), func(plain []byte) {
		_, _ = ParseCompressedUpdate(objtest.Compress(plain))
	})
}

func FuzzParseCompressedUpdateRaw(f *testing.F) {
	testfixture.FuzzBody(f, testfixture.Bodies(f, opcode.SMSGCompressedUpdateObject), func(b []byte) {
		_, _ = ParseCompressedUpdate(b)
	})
}

func FuzzSplitCompressedMoves(f *testing.F) {
	testfixture.FuzzBody(f, inflated(f, opcode.SMSGCompressedMoves), func(plain []byte) {
		_, _ = SplitCompressedMoves(objtest.Compress(plain))
	})
}

func FuzzSplitCompressedMovesRaw(f *testing.F) {
	testfixture.FuzzBody(f, testfixture.Bodies(f, opcode.SMSGCompressedMoves), func(b []byte) {
		_, _ = SplitCompressedMoves(b)
	})
}
