package outfix

import (
	"strings"
	"sync"
)

type Session struct {
	mu    sync.Mutex
	opts  Options
	proc  *Processor
	turns []TurnRecord
}

type TurnRecord struct {
	Index   int
	Role    string
	Input   int
	Output  int
	Cleaned bool
	Actions int
	Error   bool
}

func NewSession(opts Options) *Session {
	o := opts.withDefaults()
	return &Session{opts: o, proc: &Processor{opts: o}}
}

func (s *Session) ProcessTurn(role string, content string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var res Result
	var err error

	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "model":
		res, err = s.proc.Process(content)
	case "system", "user", "tool", "human":
		res, err = conservativeClean(content)
	default:
		res, err = s.proc.Process(content)
	}

	s.turns = append(s.turns, TurnRecord{
		Index:   len(s.turns),
		Role:    strings.ToLower(strings.TrimSpace(role)),
		Input:   len(content),
		Output:  len(res.Output),
		Cleaned: res.Cleaned,
		Actions: len(res.Repairs),
		Error:   err != nil,
	})
	return res, err
}

func conservativeClean(content string) (Result, error) {
	if content == "" {
		return Result{}, nil
	}
	var acts []RepairAction
	out := normalizeOutput(content, &acts)
	if strings.TrimSpace(out) == "" && strings.TrimSpace(content) != "" {
		return Result{
			Output:     content,
			Cleaned:    false,
			Repairs:    acts,
			Confidence: 0,
		}, ErrRepairFailed
	}
	return Result{
		Output:     out,
		Cleaned:    out != content,
		Repairs:    acts,
		Confidence: 0,
	}, nil
}

func (s *Session) Turns() []TurnRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]TurnRecord, len(s.turns))
	copy(cp, s.turns)
	return cp
}
