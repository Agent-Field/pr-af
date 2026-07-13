package prompts

// Ports extract_obligations and verify_obligation from reasoners/harnesses.py.

const extractObligationsPreamble = "You map the CONSISTENCY OBLIGATIONS the changed code creates — you do NOT judge them yet.\n\n" +
	"A defect is almost always a place where code at ONE location relies on something being true " +
	"at ANOTHER location, and it isn't. Your job: read the changed code and enumerate every such " +
	"cross-location reliance, so each can be checked by going and reading the other end.\n\n" +
	"For each operation the changed code performs, ask: 'for this to be correct, what must be true " +
	"ELSEWHERE?' — at the definition it calls, the place a value it passes is produced or stored, " +
	"the counterpart of a branch it takes, the real type behind an assumption it makes, or the code " +
	"that consumes what it produces. Each distinct reliance is one obligation.\n\n" +
	"Derive obligations from the STRUCTURE of this specific code — never from a remembered list of " +
	"common bugs. Be exhaustive: every call, argument, branch, type assumption, and produced/consumed " +
	"value is a candidate. Favour load-bearing reliances (security, correctness, data integrity) over " +
	"cosmetic ones. It is fine if most obligations turn out to hold — completeness now matters more.\n\n" +
	"Each obligation has three fields:\n" +
	"- where: the exact changed line/operation that creates the reliance (file + a code snippet).\n" +
	"- relies_on: a concrete description of the OTHER location or fact a verifier must GO FIND and " +
	"read — specific enough to locate it (e.g. 'the method that creates/stores these resources, to " +
	"see which key/owner they are stored under', or 'the sibling branch that handles the opposite " +
	"outcome, to compare how it is treated').\n" +
	"- property: the exact thing that must hold for the changed line to be correct (e.g. 'the lookup " +
	"key here equals the key used when the resource is stored', 'both branches treat their outcome " +
	"with the same level of trust').\n\n" +
	"Return up to 14 obligations, highest-stakes first.\n\n"

// ExtractObligationsPrompt ports extract_obligations. Caller invokes only when
// there is at least one non-empty patch.
func ExtractObligationsPrompt(diffPatches []StrPair, repoPath, prContext string) string {
	prBlock := ""
	if prContext != "" {
		prBlock = "## PR Context\n\n" + prContext + "\n\n"
	}
	return extractObligationsPreamble + prBlock + diffRef(filterPatches(diffPatches), repoPath, "obligations_diff.md")
}

// VerifyObligationPrompt ports verify_obligation's single-obligation prompt.
func VerifyObligationPrompt(where, reliesOn, property string) string {
	return "You verify ONE consistency obligation, and nothing else. This is your entire job, so do it " +
		"thoroughly: actually GO FIND and READ the other location, do not reason from memory or guess.\n\n" +
		"## The changed code relies on something elsewhere\n" +
		"- WHERE (the changed code): " + where + "\n" +
		"- IT RELIES ON: " + reliesOn + "\n" +
		"- PROPERTY THAT MUST HOLD: " + property + "\n\n" +
		"## What to do\n" +
		"1. Locate the OTHER end described in 'relies on' — search the repository, open the file, read it.\n" +
		"2. Read the changed location too. Compare the two ends.\n" +
		"3. Decide whether the PROPERTY actually holds, citing the exact code at BOTH ends.\n\n" +
		"If the property HOLDS, return holds=true (no finding needed).\n" +
		"If the property does NOT hold — the two ends disagree — return holds=false and fill the finding " +
		"fields: a precise title, severity (critical/important/suggestion/nitpick), file_path + line_start " +
		"of the changed location, body (state both ends explicitly and exactly how they disagree, and the " +
		"concrete consequence that follows), evidence (quote the code from BOTH ends), and a suggestion. " +
		"Only report holds=false if you VERIFIED the disagreement in the real code. confidence >= 0.6."
}
