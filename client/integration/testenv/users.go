/*
Copyright 2026 Bitasylum

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

package testenv

import (
	"context"
	"fmt"
	"os"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// Roles in this repository that the tests bind users to.
const (
	// WriterRole is the mode manager's role. Its resourceNames are replaced
	// with the ClusterMode a test uses.
	WriterRole     = "config/mode-manager/rbac/clustermode_writer_role.yaml"
	MaintainerRole = "config/rbac/clustermode_maintainer_role.yaml"
	ViewerRole     = "config/rbac/clustermode_viewer_role.yaml"
	// DefinitionViewerRole lets a user read ClusterModeDefinitions.
	DefinitionViewerRole = "config/rbac/clustermodedefinition_viewer_role.yaml"
)

// Grant is a role to bind a user to.
type Grant struct {
	// Role is a ClusterRole file in this repository, such as [WriterRole].
	Role string
	// ResourceNames, when set, replaces the resourceNames of every rule that
	// has them.
	ResourceNames []string
	// Rules, when Role is empty, are the rules of a role made for the user.
	Rules []rbacv1.PolicyRule
	// Namespace, when set, makes the grant a Role and RoleBinding in that
	// namespace instead of cluster-wide.
	Namespace string
}

// User returns a config for a new user bound to the given roles, and the
// user's name.
func (e *Env) User(ctx context.Context, grants ...Grant) (*rest.Config, string, error) {
	name := e.Name("user")
	user, err := e.env.AddUser(envtest.User{Name: name}, nil)
	if err != nil {
		return nil, "", err
	}
	for i, g := range grants {
		role, err := e.clusterRole(g)
		if err != nil {
			return nil, "", err
		}
		role.Name = fmt.Sprintf("%s-%d", name, i)
		if err := e.bind(ctx, name, role, g.Namespace); err != nil {
			return nil, "", err
		}
	}
	if err := e.waitForRBAC(ctx, name, grants); err != nil {
		return nil, "", err
	}
	return user.Config(), name, nil
}

// bind creates role for the user, as a ClusterRole, or a Role in namespace.
func (e *Env) bind(ctx context.Context, user string, role *rbacv1.ClusterRole, namespace string) error {
	subjects := []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: user}}
	if namespace != "" {
		r := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace}, Rules: role.Rules}
		if _, err := e.Kube.RbacV1().Roles(namespace).Create(ctx, r, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating Role %s: %w", role.Name, err)
		}
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
			Subjects:   subjects,
		}
		if _, err := e.Kube.RbacV1().RoleBindings(namespace).Create(ctx, rb, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating RoleBinding %s: %w", role.Name, err)
		}
		return nil
	}
	if _, err := e.Kube.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating ClusterRole %s: %w", role.Name, err)
	}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		Subjects:   subjects,
	}
	if _, err := e.Kube.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating ClusterRoleBinding %s: %w", role.Name, err)
	}
	return nil
}

func (e *Env) clusterRole(g Grant) (*rbacv1.ClusterRole, error) {
	if g.Role == "" {
		return &rbacv1.ClusterRole{Rules: g.Rules}, nil
	}
	data, err := os.ReadFile(e.Path(g.Role))
	if err != nil {
		return nil, err
	}
	role := &rbacv1.ClusterRole{}
	if err := yaml.Unmarshal(data, role); err != nil {
		return nil, fmt.Errorf("reading %s: %w", g.Role, err)
	}
	if g.ResourceNames != nil {
		for i := range role.Rules {
			if len(role.Rules[i].ResourceNames) > 0 {
				role.Rules[i].ResourceNames = g.ResourceNames
			}
		}
	}
	role.ObjectMeta = metav1.ObjectMeta{}
	return role, nil
}

// waitForRBAC waits until the authorizer allows the first rule of each
// grant: RBAC changes reach it moments after they are made.
func (e *Env) waitForRBAC(ctx context.Context, user string, grants []Grant) error {
	for _, g := range grants {
		role, err := e.clusterRole(g)
		if err != nil {
			return err
		}
		if len(role.Rules) == 0 {
			continue
		}
		rule := role.Rules[0]
		attrs := &authorizationv1.ResourceAttributes{
			Group: first(rule.APIGroups), Resource: first(rule.Resources), Verb: first(rule.Verbs),
			Name: first(rule.ResourceNames), Namespace: g.Namespace,
		}
		err = wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true,
			func(ctx context.Context) (bool, error) {
				review, err := e.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx,
					&authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
						User: user, ResourceAttributes: attrs,
					}}, metav1.CreateOptions{})
				if err != nil {
					return false, err
				}
				return review.Status.Allowed, nil
			})
		if err != nil {
			return fmt.Errorf("waiting for %s to be allowed to %s %s: %w", user, attrs.Verb, attrs.Resource, err)
		}
	}
	return nil
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
