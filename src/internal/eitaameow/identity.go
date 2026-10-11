package eitaameow

func (protocolRules) ValidateMessageID(id string) bool {
	_, err := messageNumber(id)
	return err == nil
}
