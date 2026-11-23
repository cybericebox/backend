-- The human defender's dossier of a test lab as it was deployed (console design 9a.3): the console links of the seat, the
-- forbidden list, the topology mode and the operator credentials, the secret ones sealed as stored. A later edit of the draft
-- never changes it. Only a column is added, so old and new pods run side by side during a rolling update.
ALTER TABLE exercise_test_deployments ADD COLUMN dossier jsonb;
