// Package planlint lints a dungeon plan against the creature export of its
// map (creatures.tsv, written by tools/dungeongen/export.sh): the creature
// check (Check), the pull-size lint (PullSizes), the route lint
// (RouteIntoLaterPacks), the pack-overlap lint (PackOverlaps) and the six
// walk-ready lints (WalkReady). It is the lint core shared by the dungeongen
// -check flag and the plan-check command: both print the same findings and
// use the same exit codes.
package planlint
