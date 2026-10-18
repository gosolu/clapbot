package builtin

import (
	"embed"

	"github.com/gosolu/react"
)

//go:embed *
var FS embed.FS

// SkillSource returns a react.SkillSource for the bundled built-in skills.
func SkillSource() react.SkillSource {
	return react.SkillFS(FS, ".")
}
