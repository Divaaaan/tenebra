package securefs

func verifyObjectInfoPrivate(root *rootState) bool {
	info, err := root.ops.inspect(root.h)
	return err == nil && info.kind == kindDirectory && verifyObject(info, root.policy, root.actor, false) == nil
}
