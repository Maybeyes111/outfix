// chaos_amd64.s — hand-assembled x86-64 "radical AI" garbage generator.
// xorshift64* PRNG + byte-class mixer. Character tables are raw binary/hex
// literals assembled by hand. No libc, no runtime calls.

#include "textflag.h"

// func fill(ptr *byte, n int, seed uint64)
TEXT ·fill(SB), NOSPLIT|NOFRAME, $0-24
	MOVQ	ptr+0(FP), DI
	MOVQ	n+8(FP), SI
	MOVQ	seed+16(FP), AX
	TESTQ	SI, SI
	JLE	done

loop:
	MOVQ	AX, R8
	SHRQ	$12, R8
	XORQ	R8, AX
	MOVQ	AX, R8
	SHLQ	$25, R8
	XORQ	R8, AX
	MOVQ	AX, R8
	SHRQ	$27, R8
	XORQ	R8, AX
	MOVQ	$0x2545F4914F6CDD1D, R9
	IMULQ	R9, AX

	MOVQ	AX, R10
	ANDQ	$15, R10

	CMPQ	R10, $0
	JE	class_struct
	CMPQ	R10, $1
	JE	class_lt
	CMPQ	R10, $2
	JE	class_slash
	CMPQ	R10, $3
	JE	class_word
	CMPQ	R10, $4
	JE	class_digit
	CMPQ	R10, $5
	JE	class_newline
	CMPQ	R10, $6
	JE	class_high
	CMPQ	R10, $7
	JE	class_quote

	MOVQ	AX, R11
	SHRQ	$40, R11
	ANDQ	$15, R11
	MOVB	$0x61, AL
	ADDB	R11B, AL
	JMP	store

class_struct:
	MOVQ	AX, R11
	SHRQ	$32, R11
	ANDQ	$7, R11
	LEAQ	structtbl(SB), R12
	MOVB	(R12)(R11*1), AL
	JMP	store

class_lt:
	MOVB	$0x3C, AL
	JMP	store

class_slash:
	MOVB	$0x2F, AL
	JMP	store

class_word:
	MOVQ	AX, R11
	SHRQ	$45, R11
	ANDQ	$3, R11
	LEAQ	wordtbl(SB), R12
	MOVB	(R12)(R11*1), AL
	JMP	store

class_digit:
	MOVQ	AX, R11
	SHRQ	$50, R11
	ANDQ	$7, R11
	ADDQ	$0x30, R11
	MOVB	R11B, AL
	JMP	store

class_newline:
	MOVB	$0x0A, AL
	JMP	store

class_high:
	MOVQ	AX, R11
	SHRQ	$56, R11
	ORB	$0xC0, R11
	MOVB	R11B, AL
	JMP	store

class_quote:
	MOVB	$0x22, AL
	JMP	store

store:
	MOVB	AL, (DI)
	INCQ	DI
	DECQ	SI
	JNZ	loop

done:
	RET

GLOBL	structtbl(SB), RODATA|NOPTR, $8
	BYTE	$0x7B; BYTE $0x7D; BYTE $0x5B; BYTE $0x5D
	BYTE	$0x22; BYTE $0x3A; BYTE $0x2C; BYTE $0x27

GLOBL	wordtbl(SB), RODATA|NOPTR, $4
	BYTE	$0x74; BYTE $0x68; BYTE $0x6E; BYTE $0x6B
