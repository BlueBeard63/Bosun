package eventmod

// Direction records whether a service produces or consumes a subject, for the
// deploy manifest (github.com/bluebeard63/bosun/modules/manifestmod).
type Direction string

const (
	Produces Direction = "produces"
	Consumes Direction = "consumes"
)

// Declaration is a declared subject dependency surfaced in the deploy manifest
// so the platform knows which queues/topics a service needs wired up.
type Declaration struct {
	Subject   string    `json:"subject"`
	Direction Direction `json:"direction"`
	Group     string    `json:"group,omitempty"`
}

var declarations []Declaration

// Declare records that this service produces or consumes a subject. Call it at
// package init next to a Topic declaration; manifestmod reads Declarations().
//
//	var UserCreated = eventmod.Topic[User]{Subject: "users.created"}
//	var _ = eventmod.Declare("users.created", eventmod.Produces, "")
func Declare(subject string, dir Direction, group string) struct{} {
	declarations = append(declarations, Declaration{Subject: subject, Direction: dir, Group: group})
	return struct{}{}
}

// Declarations returns every declared subject dependency. manifestmod reads this.
func Declarations() []Declaration {
	out := make([]Declaration, len(declarations))
	copy(out, declarations)
	return out
}
