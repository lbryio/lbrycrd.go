package cmd

import (
	"fmt"

	"github.com/btcsuite/btcd/claimtrie"
	"github.com/btcsuite/btcd/claimtrie/block"
	"github.com/btcsuite/btcd/claimtrie/block/blockrepo"
	"github.com/btcsuite/btcd/claimtrie/chain/chainrepo"
	"github.com/btcsuite/btcd/claimtrie/change"
	"github.com/btcsuite/btcd/claimtrie/config"
	"github.com/btcsuite/btcd/claimtrie/node"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(chainCmd)
}

var chainCmd = &cobra.Command{
	Use:   "chain",
	Short: "chain related command",
	RunE: func(cmd *cobra.Command, args []string) error {

		ct, err := claimtrie.New(false)
		if err != nil {
			return fmt.Errorf("create claimtrie: %w", err)
		}
		defer ct.Close()

		cfg := config.Config

		changeRepo, err := chainrepo.NewPostgres(cfg.ChainRepoPostgres.DSN, false)
		if err != nil {
			return fmt.Errorf("open change repo: %w", err)
		}

		blockRepo, err := blockrepo.NewPebble(cfg.BlockRepoPebble.Path)
		if err != nil {
			return fmt.Errorf("open block repo: %w", err)
		}

		targetHeight := int32(10000)

		for height := int32(0); height < targetHeight; height++ {

			changes, err := changeRepo.LoadByHeight(height)
			if err != nil {
				return fmt.Errorf("load from change repo: %w", err)
			}

			for _, chg := range changes {
				if chg.Height != ct.Height() {
					err = appendBlock(ct, blockRepo)
					if err != nil {
						return err
					}
					if ct.Height()%1000 == 0 {
						fmt.Printf("\rblock: %d", ct.Height())
					}
				}

				switch chg.Type {
				case change.AddClaim:
					op := *node.NewOutPointFromString(chg.OutPoint)
					err = ct.AddClaim(chg.Name, op, chg.Amount, chg.Value)

				case change.UpdateClaim:
					op := *node.NewOutPointFromString(chg.OutPoint)
					claimID, _ := node.NewIDFromString(chg.ClaimID)
					id := claimID
					err = ct.UpdateClaim(chg.Name, op, chg.Amount, id, chg.Value)

				case change.SpendClaim:
					op := *node.NewOutPointFromString(chg.OutPoint)
					err = ct.SpendClaim(chg.Name, op)

				case change.AddSupport:
					op := *node.NewOutPointFromString(chg.OutPoint)
					claimID, _ := node.NewIDFromString(chg.ClaimID)
					id := claimID
					err = ct.AddSupport(chg.Name, op, chg.Amount, id)

				case change.SpendSupport:
					op := *node.NewOutPointFromString(chg.OutPoint)
					err = ct.SpendClaim(chg.Name, op)

				default:
					err = fmt.Errorf("invalid command: %v", chg)
				}

				if err != nil {
					return fmt.Errorf("execute command %v: %w", chg, err)
				}
			}
		}

		return nil
	},
}

func appendBlock(ct *claimtrie.ClaimTrie, blockRepo block.Repo) error {

	err := ct.AppendBlock()
	if err != nil {
		return fmt.Errorf("append block: %w", err)

	}

	height := ct.Height()

	hash, err := blockRepo.Get(height)
	if err != nil {
		return fmt.Errorf("load from block repo: %w", err)
	}

	if ct.MerkleHash() != hash {
		return fmt.Errorf("hash mismatched at height %5d: exp: %s, got: %s", height, hash, ct.MerkleHash())
	}

	return nil
}
