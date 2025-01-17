/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package chartutil

import (
	"fmt"
	"io"
	"io/ioutil"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/mitchellh/copystructure"
	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"

	"helm.sh/helm/v3/pkg/chart"
)

// GlobalKey is the name of the Values key that is used for storing global vars.
const GlobalKey = "global"

// Values represents a collection of chart values.
type Values map[string]interface{}

// YAML encodes the Values into a YAML string.
func (v Values) YAML() (string, error) {
	b, err := yaml.Marshal(v)
	return string(b), err
}

// Table gets a table (YAML subsection) from a Values object.
//
// The table is returned as a Values.
//
// Compound table names may be specified with dots:
//
//	foo.bar
//
// The above will be evaluated as "The table bar inside the table
// foo".
//
// An ErrNoTable is returned if the table does not exist.
func (v Values) Table(name string) (Values, error) {
	table := v
	var err error

	for _, n := range parsePath(name) {
		if table, err = tableLookup(table, n); err != nil {
			break
		}
	}
	return table, err
}

// AsMap is a utility function for converting Values to a map[string]interface{}.
//
// It protects against nil map panics.
func (v Values) AsMap() map[string]interface{} {
	if len(v) == 0 {
		return map[string]interface{}{}
	}
	return v
}

// Encode writes serialized Values information to the given io.Writer.
func (v Values) Encode(w io.Writer) error {
	out, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// Clone values
func (v Values) MustClone() Values {
	r, err := copystructure.Copy(v)
	if err != nil {
		panic(err)
	}
	return r.(Values)
}

func tableLookup(v Values, simple string) (Values, error) {
	v2, ok := v[simple]
	if !ok {
		return v, ErrNoTable{simple}
	}
	if vv, ok := v2.(map[string]interface{}); ok {
		return vv, nil
	}

	// This catches a case where a value is of type Values, but doesn't (for some
	// reason) match the map[string]interface{}. This has been observed in the
	// wild, and might be a result of a nil map of type Values.
	if vv, ok := v2.(Values); ok {
		return vv, nil
	}

	return Values{}, ErrNoTable{simple}
}

// ReadValues will parse YAML byte data into a Values.
func ReadValues(data []byte) (vals Values, err error) {
	err = yaml.Unmarshal(data, &vals)
	if len(vals) == 0 {
		vals = Values{}
	}
	return vals, err
}

// ReadValuesFile will parse a YAML file into a map of values.
func ReadValuesFile(filename string) (Values, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return map[string]interface{}{}, err
	}
	return ReadValues(data)
}

// ReleaseOptions represents the additional release options needed
// for the composition of the final values struct
type ReleaseOptions struct {
	Name      string
	Namespace string
	Revision  int
	IsUpgrade bool
	IsInstall bool
}

// ToRenderValues composes the struct from the data coming from the Releases, Charts and Values files
//
// This takes both ReleaseOptions and Capabilities to merge into the render values.
func ToRenderValues(chrt *chart.Chart, chrtVals map[string]interface{}, options ReleaseOptions, caps *Capabilities) (Values, error) {
	if caps == nil {
		caps = DefaultCapabilities
	}
	top := map[string]interface{}{
		"Chart":        chrt.Metadata,
		"Capabilities": caps,
		"Release": map[string]interface{}{
			"Name":      options.Name,
			"FullName":  chrt.FullName(),
			"Namespace": options.Namespace,
			"IsUpgrade": options.IsUpgrade,
			"IsInstall": options.IsInstall,
			"Revision":  options.Revision,
			"Service":   "Helm",
		},
	}

	vals, err := CoalesceValues(chrt, chrtVals)
	if err != nil {
		return top, err
	}

	if v, err := applyTemplateValues(chrt, top, vals); err == nil {
		vals = v
	} else {
		return nil, errors.Wrapf(err, "cannot load %s: %s", chrt.TemplateValues.Name, err.Error())
	}

	if err := ValidateAgainstSchema(chrt, vals); err != nil {
		errFmt := "values don't meet the specifications of the schema(s) in the following chart(s):\n%s"
		return top, fmt.Errorf(errFmt, err.Error())
	}

	top["Values"] = vals
	return top, nil
}

// istable is a special-purpose function to see if the present thing matches the definition of a YAML table.
func istable(v interface{}) bool {
	_, ok := v.(map[string]interface{})
	return ok
}

// PathValue takes a path that traverses a YAML structure and returns the value at the end of that path.
// The path starts at the root of the YAML structure and is comprised of YAML keys separated by periods.
// Given the following YAML data the value at path "chapter.one.title" is "Loomings".
//
//	chapter:
//	  one:
//	    title: "Loomings"
func (v Values) PathValue(path string) (interface{}, error) {
	if path == "" {
		return nil, errors.New("YAML path cannot be empty")
	}
	return v.pathValue(parsePath(path))
}

func (v Values) pathValue(path []string) (interface{}, error) {
	if len(path) == 1 {
		// if exists must be root key not table
		if _, ok := v[path[0]]; ok && !istable(v[path[0]]) {
			return v[path[0]], nil
		}
		return nil, ErrNoValue{path[0]}
	}

	key, path := path[len(path)-1], path[:len(path)-1]
	// get our table for table path
	t, err := v.Table(joinPath(path...))
	if err != nil {
		return nil, ErrNoValue{key}
	}
	// check table for key and ensure value is not a table
	if k, ok := t[key]; ok && !istable(k) {
		return k, nil
	}
	return nil, ErrNoValue{key}
}

func parsePath(key string) []string { return strings.Split(key, ".") }

func joinPath(path ...string) string { return strings.Join(path, ".") }

func buildContext(cxt Values, chrt *chart.Chart) Values {
	if rv, err := cxt.Table("Release"); err == nil {
		rv = rv.MustClone()
		rv["Name"] = chrt.Name()
		rv["FullName"] = chrt.FullName()
		return Values{
			"Chart":        chrt.Metadata,
			"Capabilities": cxt["Capabilities"],
			"Release":      rv,
		}
	}
	return nil
}

func applyTemplateValues(chrt *chart.Chart, cxt Values, vals Values) (Values, error) {
	for _, item := range chrt.Dependencies() {
		if vls, err := vals.Table(item.Name()); err == nil {
			if v, err := applyTemplateValues(item, buildContext(cxt, item), vls); err == nil {
				vals[item.Name()] = v.AsMap()
			} else {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	if chrt.TemplateValues != nil {
		t := template.New("gotpl")
		t.Funcs(sprig.TxtFuncMap())

		if _, err := t.New(chrt.TemplateValues.Name).Parse(string(chrt.TemplateValues.Data)); err != nil {
			return nil, err
		}
		v, err := copystructure.Copy(cxt)
		if err != nil {
			return nil, err
		}
		topCopy := v.(Values)
		topCopy["Values"] = vals.AsMap()
		var buffer strings.Builder
		if err := t.ExecuteTemplate(&buffer, chrt.TemplateValues.Name, topCopy); err != nil {
			return nil, err
		}
		var overrideVals Values
		if err := yaml.UnmarshalStrict([]byte(buffer.String()), &overrideVals); err != nil {
			return nil, errors.Wrapf(err, "cannot load %s", chrt.TemplateValues.Name)
		}
		return CoalesceTables(overrideVals, vals), nil
	}
	return vals, nil
}
