package autonomy

type CompletionPolicyDef struct {
	TaskType TaskType
	Contract string
}

type CompletionPolicyManager struct {
	Policies []CompletionPolicyDef
}

func NewCompletionPolicyManager() *CompletionPolicyManager {
	return &CompletionPolicyManager{
		Policies: []CompletionPolicyDef{},
	}
}

func (m *CompletionPolicyManager) GetPolicy(taskType TaskType) CompletionPolicyDef {
	for _, policy := range m.Policies {
		if policy.TaskType == taskType {
			return policy
		}
	}
	return CompletionPolicyDef{}
}

func (m *CompletionPolicyManager) AddPolicy(policy CompletionPolicyDef) {
	m.Policies = append(m.Policies, policy)
}
