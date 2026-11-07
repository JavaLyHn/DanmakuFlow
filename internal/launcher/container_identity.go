package launcher

type ContainerIdentity struct {
	ID            string `json:"id"`
	StartedAt     string `json:"startedAt"`
	TermRequested bool   `json:"termRequested,omitempty"`
}
