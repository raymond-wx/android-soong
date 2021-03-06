// Copyright 2015 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package android

import (
	"fmt"

	"github.com/google/blueprint"
)

type moduleType struct {
	name    string
	factory ModuleFactory
}

var moduleTypes []moduleType

type singleton struct {
	name    string
	factory SingletonFactory
}

var singletons []singleton
var preSingletons []singleton

type mutator struct {
	name            string
	bottomUpMutator blueprint.BottomUpMutator
	topDownMutator  blueprint.TopDownMutator
	parallel        bool
}

type ModuleFactory func() Module

// ModuleFactoryAdaptor wraps a ModuleFactory into a blueprint.ModuleFactory by converting a Module
// into a blueprint.Module and a list of property structs
func ModuleFactoryAdaptor(factory ModuleFactory) blueprint.ModuleFactory {
	return func() (blueprint.Module, []interface{}) {
		module := factory()
		return module, module.GetProperties()
	}
}

type SingletonFactory func() Singleton

// SingletonFactoryAdaptor wraps a SingletonFactory into a blueprint.SingletonFactory by converting
// a Singleton into a blueprint.Singleton
func SingletonFactoryAdaptor(ctx *Context, factory SingletonFactory) blueprint.SingletonFactory {
	return func() blueprint.Singleton {
		singleton := factory()
		if makevars, ok := singleton.(SingletonMakeVarsProvider); ok {
			registerSingletonMakeVarsProvider(ctx.config, makevars)
		}
		return &singletonAdaptor{Singleton: singleton}
	}
}

func RegisterModuleType(name string, factory ModuleFactory) {
	moduleTypes = append(moduleTypes, moduleType{name, factory})
}

func RegisterSingletonType(name string, factory SingletonFactory) {
	singletons = append(singletons, singleton{name, factory})
}

func RegisterPreSingletonType(name string, factory SingletonFactory) {
	preSingletons = append(preSingletons, singleton{name, factory})
}

type Context struct {
	*blueprint.Context
	config Config
}

func NewContext(config Config) *Context {
	ctx := &Context{blueprint.NewContext(), config}
	ctx.SetSrcDir(absSrcDir)
	return ctx
}

// RegisterForBazelConversion registers an alternate shadow pipeline of
// singletons, module types and mutators to register for converting Blueprint
// files to semantically equivalent BUILD files.
func (ctx *Context) RegisterForBazelConversion() {
	for _, t := range moduleTypes {
		ctx.RegisterModuleType(t.name, ModuleFactoryAdaptor(t.factory))
	}

	bazelConverterSingleton := singleton{"bp2build", BazelConverterSingleton}
	ctx.RegisterSingletonType(bazelConverterSingleton.name,
		SingletonFactoryAdaptor(ctx, bazelConverterSingleton.factory))

	registerMutatorsForBazelConversion(ctx.Context)
}

func (ctx *Context) Register() {
	for _, t := range preSingletons {
		ctx.RegisterPreSingletonType(t.name, SingletonFactoryAdaptor(ctx, t.factory))
	}

	for _, t := range moduleTypes {
		ctx.RegisterModuleType(t.name, ModuleFactoryAdaptor(t.factory))
	}

	for _, t := range singletons {
		ctx.RegisterSingletonType(t.name, SingletonFactoryAdaptor(ctx, t.factory))
	}

	registerMutators(ctx.Context, preArch, preDeps, postDeps, finalDeps)

	ctx.RegisterSingletonType("bazeldeps", SingletonFactoryAdaptor(ctx, BazelSingleton))

	// Register phony just before makevars so it can write out its phony rules as Make rules
	ctx.RegisterSingletonType("phony", SingletonFactoryAdaptor(ctx, phonySingletonFactory))

	// Register makevars after other singletons so they can export values through makevars
	ctx.RegisterSingletonType("makevars", SingletonFactoryAdaptor(ctx, makeVarsSingletonFunc))

	// Register env and ninjadeps last so that they can track all used environment variables and
	// Ninja file dependencies stored in the config.
	ctx.RegisterSingletonType("env", SingletonFactoryAdaptor(ctx, EnvSingleton))
	ctx.RegisterSingletonType("ninjadeps", SingletonFactoryAdaptor(ctx, ninjaDepsSingletonFactory))
}

func ModuleTypeFactories() map[string]ModuleFactory {
	ret := make(map[string]ModuleFactory)
	for _, t := range moduleTypes {
		ret[t.name] = t.factory
	}
	return ret
}

// Interface for registering build components.
//
// Provided to allow registration of build components to be shared between the runtime
// and test environments.
type RegistrationContext interface {
	RegisterModuleType(name string, factory ModuleFactory)
	RegisterSingletonType(name string, factory SingletonFactory)
	PreArchMutators(f RegisterMutatorFunc)

	// Register pre arch mutators that are hard coded into mutator.go.
	//
	// Only registers mutators for testing, is a noop on the InitRegistrationContext.
	HardCodedPreArchMutators(f RegisterMutatorFunc)

	PreDepsMutators(f RegisterMutatorFunc)
	PostDepsMutators(f RegisterMutatorFunc)
	FinalDepsMutators(f RegisterMutatorFunc)
}

// Used to register build components from an init() method, e.g.
//
// init() {
//   RegisterBuildComponents(android.InitRegistrationContext)
// }
//
// func RegisterBuildComponents(ctx android.RegistrationContext) {
//   ctx.RegisterModuleType(...)
//   ...
// }
//
// Extracting the actual registration into a separate RegisterBuildComponents(ctx) function
// allows it to be used to initialize test context, e.g.
//
//   ctx := android.NewTestContext(config)
//   RegisterBuildComponents(ctx)
var InitRegistrationContext RegistrationContext = &initRegistrationContext{
	moduleTypes:    make(map[string]ModuleFactory),
	singletonTypes: make(map[string]SingletonFactory),
}

// Make sure the TestContext implements RegistrationContext.
var _ RegistrationContext = (*TestContext)(nil)

type initRegistrationContext struct {
	moduleTypes    map[string]ModuleFactory
	singletonTypes map[string]SingletonFactory
}

func (ctx *initRegistrationContext) RegisterModuleType(name string, factory ModuleFactory) {
	if _, present := ctx.moduleTypes[name]; present {
		panic(fmt.Sprintf("module type %q is already registered", name))
	}
	ctx.moduleTypes[name] = factory
	RegisterModuleType(name, factory)
}

func (ctx *initRegistrationContext) RegisterSingletonType(name string, factory SingletonFactory) {
	if _, present := ctx.singletonTypes[name]; present {
		panic(fmt.Sprintf("singleton type %q is already registered", name))
	}
	ctx.singletonTypes[name] = factory
	RegisterSingletonType(name, factory)
}

func (ctx *initRegistrationContext) PreArchMutators(f RegisterMutatorFunc) {
	PreArchMutators(f)
}

func (ctx *initRegistrationContext) HardCodedPreArchMutators(f RegisterMutatorFunc) {
	// Nothing to do as the mutators are hard code in preArch in mutator.go
}

func (ctx *initRegistrationContext) PreDepsMutators(f RegisterMutatorFunc) {
	PreDepsMutators(f)
}

func (ctx *initRegistrationContext) PostDepsMutators(f RegisterMutatorFunc) {
	PostDepsMutators(f)
}

func (ctx *initRegistrationContext) FinalDepsMutators(f RegisterMutatorFunc) {
	FinalDepsMutators(f)
}
