src = src.replace(
    '''		ev.Target = audit.Target{Kind: TargetKindCredential, ID: cred.PublicID()}
		return s.audit.RecordTx(ctx, tx, *ev)''',
    '''		ev.Target = audit.Target{Kind: TargetKindCredential, ID: cred.PublicID()}
		if ev.Extra == nil {
			ev.Extra = map[string]any{}
		}
		ev.Extra["token"] = minted.Token
		return s.audit.RecordTx(ctx, tx, *ev)''')
