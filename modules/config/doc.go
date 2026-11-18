// Package config answers one question: which upstream models
// the semantic tiers the user wrote ultimately correspond to.
//
// It is layered as "facts -> rules -> IO":
// - domain defines Provider, Profile, Binding and State; these values read no files;
// - resolve combines profiles, tiers and health state into an ordered candidate chain;
// - store/paths fetches facts from disk and saves atomically;
// - roleprov receives dynamic tiers contributed by other modules.
//
// The top-level Config capability opens only one write entry point, "register
// dynamic tiers". The gateway consumes an already resolved config snapshot, and
// client env injection belongs to runtime; this way the config module does not
// gradually become a central object that knows HTTP, processes and every plugin.
package config
