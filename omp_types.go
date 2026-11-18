package snapcompact

import "encoding/json"

// OMPVersion identifies the reference implementation for the archive API.
const OMPVersion = "18.22.2"

const ompDimOn, ompDimOff, ompNewline = '\x0e', '\x0f', '█'

// OMPMessage is the text-bearing subset of OMP's model message format.
// Tool result IDs associate results with previously archived tool calls.
type OMPMessage struct {
	Role, ToolCallID string
	Content          []OMPContent
	Useless, IsError bool
}

type OMPContent struct {
	Type, Text, ID, Name, Intent string
	Arguments                    json.RawMessage
}

// OMPShape describes one of the upstream grid, font and ink variants.
type OMPShape struct {
	Name, Font                              string
	CellWidth, CellHeight, Repeat, Edge     int
	SentenceInk, DimStopwords, Doc, Stretch bool
}

// OMPTarget describes the model and API spelling used to select archive policy.
// MaxEdge can carry a host-specific catalog ceiling.
type OMPTarget struct {
	Model, Provider, API string
	MaxEdge              int
}

type OMPOptions struct {
	Target                   OMPTarget
	Variant                  string
	MaxFrames                int
	HistoryTokens            int64
	MaxImageBytes            int64
	IncludeThinking          bool
	RejectUnsupported        bool
	Serialization            *OMPSerializeConfig
	Files                    OMPFileOperations
	Previous                 *OMPArchive
	PreviousSummary          string
	ReadFiles, ModifiedFiles []string
}

type OMPFrame struct {
	Shape                  OMPShape
	Cols, Rows, Characters int
	Width, Height          int
	PNG                    []byte
	Detail                 string
}

// OMPArchive carries the retained serialized source plus its rendered frames.
type OMPArchive struct {
	Shape                    OMPShape
	Text, Head, Tail         string
	Frames                   []OMPFrame
	Characters, Truncated    int
	ImageTokens              int64
	ImageTokensVerified      bool
	TokenBasis               string
	Summary                  string
	ReadFiles, ModifiedFiles []string
}

// OMPBlock occupies one position in the archive's chronological content.
type OMPBlock struct {
	Text  string
	Frame *OMPFrame
}
