package control

import "github.com/Divaaaan/tenebra/core/state"

type NodeRefView struct {
	ProfileID  string `json:"profile_id"`
	NodeID     string `json:"node_id,omitempty"`
	Resolution string `json:"resolution"`
	IssueID    string `json:"issue_id,omitempty"`
}
type SelectionView struct {
	Kind string       `json:"kind"`
	Ref  *NodeRefView `json:"ref,omitempty"`
}
type BindingView struct {
	ProfileID string      `json:"profile_id"`
	Entry     NodeRefView `json:"entry"`
	Exit      NodeRefView `json:"exit"`
}
type MultihopView struct {
	Enabled  bool          `json:"enabled"`
	Bindings []BindingView `json:"bindings"`
}

func refView(ref state.NodeRef) NodeRefView {
	return NodeRefView{ref.ProfileID, ref.NodeID, string(ref.Resolution), ref.IssueID}
}
func selectionView(selection state.Selection) *SelectionView {
	view := &SelectionView{Kind: string(selection.Kind)}
	if selection.Kind == state.Pinned {
		ref := refView(selection.Ref)
		view.Ref = &ref
	}
	return view
}
func multihopView(requirement state.MultihopRequirement) *MultihopView {
	view := &MultihopView{Enabled: requirement.Enabled, Bindings: make([]BindingView, 0, len(requirement.Bindings))}
	for _, binding := range requirement.Bindings {
		view.Bindings = append(view.Bindings, BindingView{binding.ProfileID, refView(binding.Entry), refView(binding.Exit)})
	}
	return view
}
func cloneIdentityView(s State) State {
	if s.Capabilities != nil {
		capabilities := *s.Capabilities
		s.Capabilities = &capabilities
	}
	s.SplitApps = append([]string(nil), s.SplitApps...)
	s.RulesDirect = append([]string(nil), s.RulesDirect...)
	s.RulesProxy = append([]string(nil), s.RulesProxy...)
	if s.Multihop != nil {
		multihop := *s.Multihop
		s.Multihop = &multihop
	}
	if s.CrashReports != nil {
		crashReports := *s.CrashReports
		s.CrashReports = &crashReports
	}
	if s.Selection != nil {
		selection := *s.Selection
		s.Selection = &selection
		if selection.Ref != nil {
			ref := *selection.Ref
			selection.Ref = &ref
		}
	}
	if s.MultihopV2 != nil {
		multihop := *s.MultihopV2
		multihop.Bindings = append([]BindingView{}, multihop.Bindings...)
		s.MultihopV2 = &multihop
	}
	return s
}
