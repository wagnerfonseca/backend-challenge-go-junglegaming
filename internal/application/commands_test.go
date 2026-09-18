package application_test

import (
	"errors"
	"testing"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/domain/financial"
)

func validWagerCommand(t *testing.T) application.SubmitWagerCommand {
	t.Helper()
	wallet, err := financial.ParseWalletID("018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b")
	if err != nil {
		t.Fatalf("parsing wallet: %v", err)
	}
	player, err := financial.ParsePlayerID("018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5c")
	if err != nil {
		t.Fatalf("parsing player: %v", err)
	}
	return application.SubmitWagerCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-001",
		IdempotencyKey:        "key-001",
		WalletID:              wallet,
		PlayerID:              player,
		RoundID:               "round-7",
		GameID:                "game-9",
		Kind:                  financial.KindBet,
		Amount:                mustMoney(t, "25.00", "BRL"),
	}
}

// C44, C45, C68, C74, C75, C218 - contract validation rejects malformed
// external commands before any persistence.
func TestSubmitWagerCommandValidation(t *testing.T) {
	lossZero := func() application.SubmitWagerCommand {
		cmd := validWagerCommand(t)
		cmd.Kind = financial.KindLoss
		cmd.Amount = mustMoney(t, "0.00", "BRL")
		return cmd
	}
	tests := []struct {
		name     string
		mutate   func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand
		wantCode application.ErrorCode
	}{
		{
			name:   "valid BET is accepted",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand { return cmd },
		},
		{
			name: "kind OPENING is rejected as external input",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.Kind = financial.KindOpening
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "kind outside the vocabulary is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.Kind = financial.Kind("TRANSFER")
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "missing idempotency key is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.IdempotencyKey = ""
				return cmd
			},
			wantCode: application.CodeIdempotencyKeyRequired,
		},
		{
			name: "LOSS with a nonzero amount is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.Kind = financial.KindLoss
				return cmd
			},
			wantCode: application.CodeInvalidLossAmount,
		},
		{
			name: "LOSS with zero amount is accepted",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				return lossZero()
			},
		},
		{
			name: "BET with zero amount is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.Amount = mustMoney(t, "0.00", "BRL")
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "WIN with zero amount is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.Kind = financial.KindWin
				cmd.Amount = mustMoney(t, "0.00", "BRL")
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "REFUND without a reference is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.Kind = financial.KindRefund
				return cmd
			},
			wantCode: application.CodeReferenceRequired,
		},
		{
			name: "ROLLBACK without a reference is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.Kind = financial.KindRollback
				return cmd
			},
			wantCode: application.CodeReferenceRequired,
		},
		{
			name: "BET with a reference is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.ReferenceExternalID = "bet-1"
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "missing provider identity is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.ProviderID = ""
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "missing external transaction identity is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.ExternalTransactionID = ""
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "missing wallet identity is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.WalletID = financial.WalletID{}
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "missing player identity is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.PlayerID = financial.PlayerID{}
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "missing round identity is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.RoundID = ""
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "missing game identity is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.GameID = ""
				return cmd
			},
			wantCode: application.CodeInvalidRequest,
		},
		{
			name: "uninitialized money is rejected",
			mutate: func(cmd application.SubmitWagerCommand) application.SubmitWagerCommand {
				cmd.Amount = moneyZeroValue()
				return cmd
			},
			wantCode: application.CodeInvalidMoney,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.mutate(validWagerCommand(t))
			err := cmd.Validate()
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !errors.Is(err, application.ErrContract) {
				t.Fatalf("error %v does not classify as ErrContract", err)
			}
			code, ok := application.ErrorCodeOf(err)
			if !ok {
				t.Fatalf("error %v has no stable code", err)
			}
			if code != tt.wantCode {
				t.Errorf("code = %s, want %s", code, tt.wantCode)
			}
		})
	}
}

func TestOpenWalletCommandValidation(t *testing.T) {
	player, err := financial.ParsePlayerID("018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5c")
	if err != nil {
		t.Fatalf("parsing player: %v", err)
	}
	tests := []struct {
		name     string
		cmd      application.OpenWalletCommand
		wantCode application.ErrorCode
	}{
		{name: "valid positive balance", cmd: application.OpenWalletCommand{PlayerID: player, InitialBalance: mustMoney(t, "1000.00", "BRL")}},
		{name: "valid zero balance", cmd: application.OpenWalletCommand{PlayerID: player, InitialBalance: mustMoney(t, "0.00", "BRL")}},
		{name: "missing player", cmd: application.OpenWalletCommand{InitialBalance: mustMoney(t, "10.00", "BRL")}, wantCode: application.CodeInvalidRequest},
		{name: "uninitialized money", cmd: application.OpenWalletCommand{PlayerID: player}, wantCode: application.CodeInvalidMoney},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cmd.Validate()
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			code, ok := application.ErrorCodeOf(err)
			if !ok || code != tt.wantCode {
				t.Fatalf("error = %v, code = %s, want %s", err, code, tt.wantCode)
			}
		})
	}
}
