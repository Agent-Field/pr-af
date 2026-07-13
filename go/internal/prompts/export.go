package prompts

// This file is the small exported construction/rendering seam the reasoners
// package (T3.1) compiles against. The evidence-taking builders
// (CompoundFinderPrompt, AdversaryPrompt, EvidenceVerifierPrompt) accept
// map[string]*OMap, but OMap's fields are unexported and omap() is package
// private — without these exports no other package could build an evidence
// map or reproduce the json.dumps context blocks it must write to
// .pr-af-context files byte-identically.

// NewOMap returns an empty insertion-ordered JSON object, ready for Set calls.
// It is the exported counterpart of the package-private omap() constructor.
func NewOMap() *OMap {
	return &OMap{vals: make(map[string]any)}
}

// PyJSON renders v exactly as Python's json.dumps(v, default=str): ", "/": "
// separators, insertion-ordered *OMap keys, ensure_ascii escaping, and Python
// float repr. Exported so the reasoners can write context files whose content
// is byte-identical to the JSON blocks the prompt builders embed or reference.
func PyJSON(v any) string {
	return pyJSON(v)
}
