package domain

// ValidateObservedSatisfaction validates the closed PathSatisfactionV1
// wire: the observed regular file has exactly the expected size. DB
// entity/spec bindings are separate.
func ValidateObservedSatisfaction(v Value) error {
	if err := Validate("PathSatisfactionV1", v); err != nil {
		return err
	}
	m := v.Object
	if m["expected_size"].Int != m["observed_object"].Object["size"].Int {
		return reader{}.fail(CodeConstraintViolated)
	}
	return nil
}
