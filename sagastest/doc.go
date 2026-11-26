// Package sagastest holds test kits for code built on package sagas. [TestCompensations] and [TestPivot] prove the
// contracts of a saga. [TestJournal] proves the contract of a journal, and [TestSecretStore] proves the contract of a
// secret store. Each kit runs the cases of one case list, each case in its own subtest. The case lists are
// [CompensationCases], [PivotCases], [JournalCases] and [SecretStoreCases].
package sagastest
