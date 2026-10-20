package transfer

// NotALeg is an SQL condition: the transaction under alias is in no transfer pair and no one-leg
// transfer. Each probe hits a unique index; NOT EXISTS over the transfer_leg view instead re-reads
// the user's whole union for every transaction, worst in a cached generic plan.
func NotALeg(alias string) string {
	return `NOT EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.out_tx_id = ` + alias + `.id)
		AND NOT EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.in_tx_id = ` + alias + `.id)
		AND NOT EXISTS (SELECT 1 FROM own_transfer ot WHERE ot.transaction_id = ` + alias + `.id)`
}
