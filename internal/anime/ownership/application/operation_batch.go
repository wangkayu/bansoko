package application

import (
	"regexp"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
)

var operationBatchIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

func validateOperationBatchID(id *string) error {
	if id != nil && !operationBatchIDPattern.MatchString(*id) {
		return &apperrors.BadRequest{Code: "INVALID_REQUEST", Message: "Invalid operation batch identifier."}
	}
	return nil
}

func summarizeOperationBatch(id *string, record observabilityport.ActivityRecord) observabilityport.ActivityRecord {
	if id != nil {
		record.SubjectType = observabilityport.OperationBatchSubject
		record.SubjectID = *id
		record.SubjectVersion = 1
		record.SubjectDisplay = ""
	}
	return record
}
